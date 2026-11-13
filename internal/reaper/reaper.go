// Package reaper expires overdue payments and records late transactions on
// expired payments for manual reconciliation (never auto-confirms).
package reaper

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/mufalito/oalgosolana/internal/store"
)

// Reaper runs the expiry tick.
type Reaper struct {
	DB       *store.Store
	USDCMint string
	Log      *slog.Logger
	Interval time.Duration
	// OnExpired is a metrics hook (count of expired payments).
	OnExpired func(n int)
	// LateDepositSweeper is watcher.SweepLateDeposit (custodial mode only).
	LateDepositSweeper func(ctx context.Context, p *store.Payment) error
}

// Run blocks until ctx is cancelled.
func (r *Reaper) Run(ctx context.Context) error {
	ticker := time.NewTicker(r.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := r.tick(ctx); err != nil && !errors.Is(err, context.Canceled) {
				r.Log.Error("reaper tick failed", "err", err.Error())
			}
		}
	}
}

func (r *Reaper) tick(ctx context.Context) error {
	r.sweepLateDeposits(ctx)
	ids, err := r.DB.ExpirePending(ctx, time.Now().UTC())
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := r.DB.EnqueueWebhook(ctx, id, "payment.expired"); err != nil {
			return err
		}
		r.Log.Info("payment expired", "payment_id", id)
	}
	if len(ids) > 0 && r.OnExpired != nil {
		r.OnExpired(len(ids))
	}
	return nil
}

// sweepLateDeposits lets the watcher sweep expired deposit payments that
// received funds (custodial duty: money must still reach the merchant).
func (r *Reaper) sweepLateDeposits(ctx context.Context) {
	if r.LateDepositSweeper == nil {
		return
	}
	payments, err := r.DB.QueryPayments(ctx, `SELECT `+store.PaymentCols+`
		FROM payments p
		WHERE p.status <> 'finalized'
		  AND p.expires_at < now()
		  AND p.expires_at > now() - interval '1 day'
		LIMIT 50`)
	if err != nil {
		r.Log.Error("late-deposit query failed", "err", err.Error())
		return
	}
	for _, p := range payments {
		if err := r.LateDepositSweeper(ctx, p); err != nil {
			r.Log.Error("late-deposit sweep failed", "payment_id", p.ID, "err", err.Error())
		}
	}
}
