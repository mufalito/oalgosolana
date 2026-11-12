package watcher

import (
	"context"

	"github.com/mufalito/oalgosolana/internal/store"
)

// SweepState describes what happened to a sent sweep.
type SweepState int

const (
	SweepPending SweepState = iota // in flight, not yet finalized
	SweepLanded                    // finalized successfully
	SweepFailed                    // failed on-chain or dropped: re-send
)

// DepositSweeper sweeps derived vaults for deposit-address payments.
type DepositSweeper interface {
	SweepVault(ctx context.Context, invoiceID string) (string, error)
	CheckSweep(ctx context.Context, sig string) (SweepState, error)
}

// DepositBalanceReader is the balance capability the detector must provide
// in deposit mode.
type DepositBalanceReader interface {
	GetBalance(ctx context.Context, address string) (uint64, error)
}

// processDeposit drives deposit-address payments using BALANCES, not
// signature history:
//
//	pending/processing: when the vault balance >= amount_base, send the
//	  sweep (the customer's own transfer created the vault and paid its
//	  rent; a full drain returns everything, rent included).
//	confirmed (sweep sent): finalize only when the sweep has actually
//	  LANDED (finalized, err==nil); a failed/dropped sweep is re-sent on a
//	  later tick until the vault is empty.
func (w *Watcher) processDeposit(ctx context.Context, p *store.Payment) error {
	balancer, ok := w.Detector.(DepositBalanceReader)
	if !ok {
		return nil
	}

	switch p.Status {
	case "pending", "processing":
		balance, err := balancer.GetBalance(ctx, p.Reference)
		if err != nil {
			return err
		}
		if balance == 0 {
			return nil
		}
		if balance < p.AmountBase {
			// nothing (or a partial payment) so far: keep watching
			if p.Status == "pending" {
				if err := w.DB.MarkProcessing(ctx, p.ID, ""); err != nil {
					return err
				}
				w.transition("pending", "processing", "")
			}
			return nil
		}
		// balance >= amount (or a full payment arrived while pending):
		// sweep, then confirm. Record an incoming signature for the receipt
		// if one is visible (best effort).
		sig := ""
		if sigs, err := w.Detector.SignaturesForAddress(ctx, p.Reference); err == nil && len(sigs) > 0 {
			sig = sigs[0]
		}
		return w.sweepDeposit(ctx, p, sig)

	case "confirmed":
		return w.awaitSweep(ctx, p)
	}
	return nil
}

// sweepDeposit sends the vault sweep and marks the payment confirmed.
func (w *Watcher) sweepDeposit(ctx context.Context, p *store.Payment, incomingSig string) error {
	if w.Sweeper == nil {
		w.Log.Warn("deposit funds present but no sweeper configured",
			"payment_id", p.ID, "reference", p.Reference)
		return nil
	}
	sweepSig, err := w.Sweeper.SweepVault(ctx, p.ID)
	if err != nil {
		// transient: retried next tick while the payment stays watchable
		w.Log.Error("sweep send failed", "payment_id", p.ID, "reference", p.Reference, "err", err.Error())
		return nil
	}
	if err := w.DB.ConfirmDeposit(ctx, p.ID, incomingSig, sweepSig, w.now()); err != nil {
		return err
	}
	w.transition("processing", "confirmed", "")
	if err := w.DB.EnqueueWebhook(ctx, p.ID, "payment.confirmed"); err != nil {
		return err
	}
	w.Log.Info("deposit payment confirmed; sweep sent",
		"payment_id", p.ID, "reference", p.Reference, "sweep_tx", sweepSig)
	return nil
}

// awaitSweep finalizes once the sweep has LANDED; a sweep that failed
// on-chain or was dropped is re-sent. A merely-unfinalized sweep is left
// alone until finality.
func (w *Watcher) awaitSweep(ctx context.Context, p *store.Payment) error {
	if p.SweepTx == "" {
		return w.sweepDeposit(ctx, p, p.TxSignature)
	}
	state, err := w.Sweeper.CheckSweep(ctx, p.SweepTx)
	if err != nil {
		return err
	}
	switch state {
	case SweepLanded:
		if err := w.DB.FinalizePayment(ctx, p.ID, w.now()); err != nil {
			return err
		}
		w.transition("confirmed", "finalized", "")
		if err := w.DB.EnqueueWebhook(ctx, p.ID, "payment.finalized"); err != nil {
			return err
		}
		w.Log.Info("deposit payment finalized (sweep landed)",
			"payment_id", p.ID, "reference", p.Reference, "sweep_tx", p.SweepTx)
		return nil
	case SweepFailed:
		balancer, ok := w.Detector.(DepositBalanceReader)
		if !ok {
			return nil
		}
		balance, err := balancer.GetBalance(ctx, p.Reference)
		if err != nil {
			return err
		}
		if balance > 0 {
			w.Log.Warn("sweep failed on-chain; vault still holds funds, re-sending",
				"payment_id", p.ID, "reference", p.Reference, "old_sweep_tx", p.SweepTx)
			return w.sweepDeposit(ctx, p, p.TxSignature)
		}
		return nil
	default: // SweepPending
		return nil
	}
}

// SweepLateDeposit handles any deposit payment whose vault still holds
// funds — expired, lapsed or otherwise. Money in a vault must always reach
// the merchant; state bookkeeping follows the money.
func (w *Watcher) SweepLateDeposit(ctx context.Context, p *store.Payment) error {
	balancer, ok := w.Detector.(DepositBalanceReader)
	if !ok {
		return nil
	}
	balance, err := balancer.GetBalance(ctx, p.Reference)
	if err != nil || balance == 0 {
		return err
	}
	w.Log.Warn("late funds in vault; sweeping",
		"payment_id", p.ID, "reference", p.Reference, "balance", balance, "status", p.Status)
	if p.Status == "confirmed" {
		return w.awaitSweep(ctx, p)
	}
	return w.sweepDeposit(ctx, p, p.TxSignature)
}
