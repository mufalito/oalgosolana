// Package watcher polls pending payments, validates candidate transactions
// strictly, and drives the payment state machine with idempotent transitions.
//
// State machine: pending → processing (candidate tx found, recorded + claimed)
// → confirmed (validated at the configured commitment) → finalized
// (escalated at finalized commitment). Terminal alternatives: expired (reaper),
// failed (validation failure with reason).
package watcher

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/mufalito/oalgosolana/internal/chain"
	"github.com/mufalito/oalgosolana/internal/store"
)

// Watcher owns the poll loop and worker pool.
type Watcher struct {
	DB       *store.Store
	Detector chain.Detector
	Cfg      WatcherConfig
	Log      *slog.Logger
	// OnTransition fires metrics hooks (from, to status).
	OnTransition func(from, to, reason string)
	// Sweeper + MasterKey are set only in deposit-address mode.
	Sweeper   DepositSweeper
	MasterKey []byte
}

// WatcherConfig parameterizes the loop.
type WatcherConfig struct {
	ConfirmationPolicy string // "confirmed" or "finalized"
	USDCMint           string
	PollInterval       time.Duration
	Workers            int
	Clock              func() time.Time // injectable for tests
}

func (w *Watcher) now() time.Time {
	if w.Cfg.Clock != nil {
		return w.Cfg.Clock()
	}
	return time.Now().UTC()
}

// Run blocks until ctx is cancelled, ticking the poll loop.
func (w *Watcher) Run(ctx context.Context) error {
	ticker := time.NewTicker(w.Cfg.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := w.pollOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
				w.Log.Error("watcher poll failed", "err", err.Error())
			}
		}
	}
}

// pollOnce claims due payments and processes them concurrently. Claims use
// FOR UPDATE SKIP LOCKED semantics via TryClaim so replicas are safe.
func (w *Watcher) pollOnce(ctx context.Context) error {
	payments, err := w.DB.QueryPayments(ctx, `SELECT `+store.PaymentCols+`
		FROM payments WHERE status IN ('pending','processing','confirmed') AND expires_at > now()
		ORDER BY id LIMIT 50`)
	if err != nil {
		return fmt.Errorf("claim: %w", err)
	}
	if len(payments) == 0 {
		return nil
	}
	sem := make(chan struct{}, max(1, w.Cfg.Workers))
	var wg sync.WaitGroup
	for _, p := range payments {
		wg.Add(1)
		sem <- struct{}{}
		go func(p *store.Payment) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := w.process(ctx, p); err != nil && !errors.Is(err, context.Canceled) {
				w.Log.Error("process payment failed", "payment_id", p.ID, "reference", p.Reference, "err", err.Error())
			}
		}(p)
	}
	wg.Wait()
	return nil
}

// process advances one payment by one step of the state machine.
func (w *Watcher) process(ctx context.Context, p *store.Payment) error {
	// Deposit-address payments follow the custodial flow.
	hasKey, err := w.DB.HasDepositKey(ctx, p.ID)
	if err != nil {
		return err
	}
	if hasKey {
		return w.processDeposit(ctx, p)
	}
	switch p.Status {
	case "pending":
		sigs, err := w.Detector.SignaturesForAddress(ctx, p.Reference)
		if err != nil {
			return fmt.Errorf("signatures: %w", err)
		}
		for _, sig := range sigs {
			// Claim the (payment, signature) pair idempotently; other
			// workers/replicas that saw the same signature do nothing.
			claimed, err := w.DB.ClaimSignature(ctx, p.ID, sig)
			if err != nil {
				return err
			}
			if !claimed {
				continue // already processed by someone else
			}
			// Record the candidate and move to processing atomically.
			if err := w.DB.MarkProcessing(ctx, p.ID, sig); err != nil {
				return err
			}
			w.transition("pending", "processing", "")
			w.Log.Info("candidate tx found", "payment_id", p.ID, "reference", p.Reference, "tx", sig)
			return nil
		}
		return nil

	case "processing":
		tx, err := w.Detector.Transaction(ctx, p.TxSignature, p.MerchantWallet, w.confirmCommitment())
		if err != nil {
			if chain.IsNotFound(err) {
				return nil // not indexed yet; retry next tick
			}
			return fmt.Errorf("tx fetch: %w", err) // TX_FETCH_FAILED → retry, not fail
		}
		return w.validateAndAdvance(ctx, p, tx)

	case "confirmed":
		if w.Cfg.ConfirmationPolicy == "finalized" {
			return nil // terminal under this policy
		}
		// Escalation pass at finalized commitment.
		tx, err := w.Detector.Transaction(ctx, p.TxSignature, p.MerchantWallet, "finalized")
		if err != nil {
			if chain.IsNotFound(err) {
				return nil // not finalized yet
			}
			return err
		}
		if r := chain.Validate(tx, p, w.Cfg.USDCMint); r.Failed() {
			return w.fail(ctx, p, string(r))
		}
		return w.finalize(ctx, p)
	}
	return nil
}

// validateAndAdvance validates a fetched candidate and transitions
// processing → confirmed (or failed) in one DB transaction.
func (w *Watcher) validateAndAdvance(ctx context.Context, p *store.Payment, tx *chain.TxInfo) error {
	now := w.now()
	reason := chain.Validate(tx, p, w.Cfg.USDCMint)
	if reason.Failed() {
		if err := w.DB.FailPayment(ctx, p.ID, string(reason), now); err != nil {
			return err
		}
		w.transition("processing", "failed", string(reason))
		w.Log.Warn("payment validation failed", "payment_id", p.ID, "reference", p.Reference,
			"reason", string(reason), "tx", tx.Signature)
		return nil
	}
	if err := w.DB.ConfirmPayment(ctx, p.ID, tx.Signature, now); err != nil {
		return err
	}
	w.transition("processing", "confirmed", "")
	if err := w.DB.EnqueueWebhook(ctx, p.ID, "payment.confirmed"); err != nil {
		return err
	}
	w.Log.Info("payment confirmed", "payment_id", p.ID, "reference", p.Reference, "tx", tx.Signature)

	if w.Cfg.ConfirmationPolicy == "finalized" {
		// single-hop policy: confirmed == finalized
		return w.finalize(ctx, p)
	}
	return nil
}

func (w *Watcher) finalize(ctx context.Context, p *store.Payment) error {
	now := w.now()
	if err := w.DB.FinalizePayment(ctx, p.ID, now); err != nil {
		return err
	}
	w.transition("confirmed", "finalized", "")
	if err := w.DB.EnqueueWebhook(ctx, p.ID, "payment.finalized"); err != nil {
		return err
	}
	w.Log.Info("payment finalized", "payment_id", p.ID, "reference", p.Reference, "tx", p.TxSignature)
	return nil
}

func (w *Watcher) fail(ctx context.Context, p *store.Payment, reason string) error {
	if err := w.DB.FailPayment(ctx, p.ID, reason, w.now()); err != nil {
		return err
	}
	w.transition(p.Status, "failed", reason)
	return nil
}

func (w *Watcher) confirmCommitment() string {
	if w.Cfg.ConfirmationPolicy == "finalized" {
		return "finalized"
	}
	return "confirmed"
}

func (w *Watcher) transition(from, to, reason string) {
	if w.OnTransition != nil {
		w.OnTransition(from, to, reason)
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
