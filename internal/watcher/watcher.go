// Package watcher drives deposit-vault payments: balance-triggered sweeps of
// derived vault addresses, with the confirmed sweep as the receipt.
//
// State machine: pending → processing (partial funds seen, amount not yet
// reached) → confirmed (vault funded ≥ amount, sweep sent) → finalized
// (sweep landed on-chain). Terminal alternative: expired (reaper); a vault
// that holds funds is always swept regardless of invoice state.
package watcher

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/mufalito/oalgosolana/internal/chain"
	"github.com/mufalito/oalgosolana/internal/store"
)

// WatcherConfig parameterizes the loop.
type WatcherConfig struct {
	PollInterval time.Duration
	Workers      int
	Clock        func() time.Time // injectable for tests
}

// Watcher owns the poll loop and worker pool.
type Watcher struct {
	DB       *store.Store
	Detector chain.Detector
	Log      *slog.Logger
	// Sweeper sweeps derived vaults.
	Sweeper DepositSweeper
	Cfg     WatcherConfig
	// OnTransition fires metrics hooks (from, to status).
	OnTransition func(from, to string)
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

// pollOnce processes every due payment, paged by id cursor so old unpaid
// invoices cannot starve newer ones. Confirmed payments stay watchable past
// expires_at until finalized (sweep finality). Rows are not locked across
// the (long) RPC calls: run one watcher per database.
func (w *Watcher) pollOnce(ctx context.Context) error {
	sem := make(chan struct{}, max(1, w.Cfg.Workers))
	var wg sync.WaitGroup
	const baseQuery = `SELECT ` + store.PaymentCols + `
		FROM payments
		WHERE (status IN ('pending','processing') AND expires_at > now())
		   OR status = 'confirmed'
		ORDER BY id`
	cursor := ""
	for {
		query := baseQuery + ` LIMIT 50`
		var args []any
		if cursor != "" {
			query = strings.Replace(baseQuery, "WHERE", "WHERE id > $1 AND", 1) + ` LIMIT 50`
			args = append(args, cursor)
		}
		payments, err := w.DB.QueryPayments(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("claim: %w", err)
		}
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
		if len(payments) < 50 {
			break
		}
		cursor = payments[len(payments)-1].ID
	}
	wg.Wait()
	return nil
}

// process advances one payment by one step of the state machine.
func (w *Watcher) process(ctx context.Context, p *store.Payment) error {
	return w.processDeposit(ctx, p)
}

func (w *Watcher) now() time.Time {
	if w.Cfg.Clock != nil {
		return w.Cfg.Clock()
	}
	return time.Now().UTC()
}

func (w *Watcher) transition(from, to string) {
	if w.OnTransition != nil {
		w.OnTransition(from, to)
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
