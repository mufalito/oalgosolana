// Command server runs the self-hosted, non-custodial Solana payment gateway.
package main

import (
	"context"
	"encoding/base64"
	"errors"
	"github.com/gagliardetto/solana-go"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/mufalito/oalgosolana/internal/api"
	"github.com/mufalito/oalgosolana/internal/apierr"
	"github.com/mufalito/oalgosolana/internal/chain"
	"github.com/mufalito/oalgosolana/internal/config"
	"github.com/mufalito/oalgosolana/internal/metrics"
	"github.com/mufalito/oalgosolana/internal/payment"
	"github.com/mufalito/oalgosolana/internal/reaper"
	"github.com/mufalito/oalgosolana/internal/store"
	"github.com/mufalito/oalgosolana/internal/vault"
	"github.com/mufalito/oalgosolana/internal/watcher"
	"github.com/mufalito/oalgosolana/internal/webhook"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(log)

	cfg, err := config.Load()
	if err != nil {
		log.Error("invalid configuration", "err", err.Error())
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Error("database init failed", "err", err.Error())
		os.Exit(1)
	}
	defer st.Close()

	detector := chain.NewRPCDetector(cfg.RPCURL, cfg.RateLimitRPS)
	detector.OnRequest = func(method string, d time.Duration) {
		metrics.RPCRequestSeconds.WithLabelValues(method).Observe(d.Seconds())
	}
	detector.OnError = func(method string) { metrics.RPCErrors.WithLabelValues(method).Inc() }

	paySvc := &payment.Service{Store: st, Cfg: cfg, Log: log}
	paySvc.OnCreated = func(currency string) { metrics.PaymentsCreated.WithLabelValues(currency).Inc() }

	replayer := func(ctx context.Context, paymentID string) error {
		found, err := st.ReplayDelivery(ctx, paymentID)
		if err != nil {
			return apierr.Errorf(500, apierr.CodeInternal, "replay failed")
		}
		if !found {
			return apierr.Errorf(404, apierr.CodeNotFound, "unknown payment id")
		}
		return nil
	}

	handler := api.NewServer(&api.Server{
		Pool:      st.Pool,
		RPCURL:    cfg.RPCURL,
		APIKeys:   cfg.APIKeys,
		Log:       log,
		RateRPS:   cfg.RateLimitRPS,
		RateBurst: cfg.RateLimitBurst,
		Payments:  paySvc,
		Replayer:  replayer,
	})

	srv := &http.Server{
		Addr:              ":" + strconv.Itoa(cfg.Port),
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	var masterKey []byte
	var depositSweeper watcher.DepositSweeper
	if cfg.DepositAddressMode {
		masterKey, _ = base64.StdEncoding.DecodeString(cfg.DepositMasterKeyRaw)
		hw, err := solana.PrivateKeyFromSolanaKeygenFile(cfg.GatewayHotWallet)
		if err != nil {
			log.Error("load GATEWAY_HOT_WALLET", "err", err.Error())
			os.Exit(1)
		}
		programID, err := solana.PublicKeyFromBase58(cfg.DepositProgramID)
		if err != nil {
			log.Error("invalid DEPOSIT_PROGRAM_ID")
			os.Exit(1)
		}
		vs := &vault.Sweeper{RPC: detector, ProgramID: programID, HotWallet: hw, ColdWallet: cfg.MerchantWallet}
		depositSweeper = &vault.DepositSweeperAdapter{S: vs}
		paySvc.VaultCreator = vs
	}

	w := &watcher.Watcher{
		DB: st, Detector: detector, Log: log,
		Sweeper: depositSweeper, MasterKey: masterKey,
		Cfg: watcher.WatcherConfig{
			ConfirmationPolicy: cfg.ConfirmationPolicy,
			USDCMint:           cfg.USDCMint(),
			PollInterval:       cfg.PollInterval,
			Workers:            cfg.WorkerCount,
		},
		OnTransition: func(from, to, reason string) {
			switch to {
			case "confirmed":
				metrics.PaymentsConfirmed.Inc()
			case "failed":
				metrics.PaymentsFailed.WithLabelValues(reason).Inc()
			}
		},
	}

	delivery := &webhook.Delivery{
		DB: st, Log: log, MaxAttempts: cfg.WebhookMaxAttempts,
		Signer: &webhook.Signer{Secret: []byte(cfg.WebhookSecret)},
		OnAttempt: func(result string, d time.Duration) {
			metrics.WebhookAttempts.WithLabelValues(result).Inc()
			metrics.WebhookDeliverySeconds.Observe(d.Seconds())
		},
	}

	rep := &reaper.Reaper{
		DB: st, Detector: detector, USDCMint: cfg.USDCMint(), Log: log,
		Interval:  10 * time.Second,
		OnExpired: func(n int) { metrics.PaymentsExpired.Add(float64(n)) },
	}

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return w.Run(gctx) })
	g.Go(func() error { return delivery.Run(gctx) })
	g.Go(func() error { return rep.Run(gctx) })
	g.Go(func() error {
		log.Info("listening", "addr", srv.Addr, "cluster", cfg.Cluster)
		err := srv.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	})

	gErr := make(chan error, 1)
	go func() { gErr <- g.Wait() }()
	select {
	case err := <-gErr:
		if err != nil {
			log.Error("background worker failed", "err", err.Error())
		}
	case <-ctx.Done():
	}
	log.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("graceful shutdown failed", "err", err.Error())
	}
	// cancel background workers and wait for drain
	<-gErr
}
