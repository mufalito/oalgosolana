package watcher

import (
	"context"
	"time"

	"github.com/mufalito/oalgosolana/internal/chain"
	"github.com/mufalito/oalgosolana/internal/store"
	"github.com/mufalito/oalgosolana/internal/wallet"
)

// DepositSweeper moves deposit-address funds to the merchant wallet and
// reports sweep finality (implemented by internal/sweep; faked in tests).
type DepositSweeper interface {
	Sweep(ctx context.Context, secret []byte) (string, error)
	Finalized(ctx context.Context, sig string) (bool, error)
}

// processDeposit drives deposit-address-mode payments:
//
//	pending/processing: poll the deposit address, sum incoming lamports,
//	  confirm once >= amount_base (attribution is by unique address, so
//	  overpayment and multiple transfers are tolerated).
//	confirmed: sweep full balance to merchant, then finalize when the
//	  sweep itself is finalized.
func (w *Watcher) processDeposit(ctx context.Context, p *store.Payment) error {
	keyRow, err := w.DB.GetDepositKey(ctx, p.ID)
	if err != nil {
		return err
	}
	if keyRow == nil {
		return nil // not a deposit payment
	}

	switch p.Status {
	case "pending", "processing":
		sigs, err := w.Detector.SignaturesForAddress(ctx, p.Reference)
		if err != nil {
			return err
		}
		total := uint64(0)
		confirmedSig := ""
		for _, sig := range sigs {
			claimed, err := w.DB.ClaimSignature(ctx, p.ID, sig)
			if err != nil {
				return err
			}
			if !claimed {
				// already recorded: include its delta in the running total
			}
			tx, err := w.Detector.Transaction(ctx, sig, p.Reference, "confirmed")
			if err != nil {
				if chain.IsNotFound(err) {
					continue
				}
				return err
			}
			total += tx.RecipientDeltaLamports
			confirmedSig = sig
		}
		if total == 0 {
			return nil
		}
		if p.Status == "pending" {
			if err := w.DB.MarkProcessing(ctx, p.ID, confirmedSig); err != nil {
				return err
			}
			w.transition("pending", "processing", "")
		}
		if total >= p.AmountBase {
			return w.confirmDeposit(ctx, p, confirmedSig)
		}
		return nil // partial payment so far; keep watching

	case "confirmed":
		return w.sweepDeposit(ctx, p, keyRow)
	}
	return nil
}

func (w *Watcher) confirmDeposit(ctx context.Context, p *store.Payment, sig string) error {
	if err := w.DB.ConfirmPayment(ctx, p.ID, sig, w.now()); err != nil {
		return err
	}
	w.transition("processing", "confirmed", "")
	if err := w.DB.EnqueueWebhook(ctx, p.ID, "payment.confirmed"); err != nil {
		return err
	}
	w.Log.Info("deposit payment confirmed", "payment_id", p.ID, "reference", p.Reference, "tx", sig)
	keyRow, err := w.DB.GetDepositKey(ctx, p.ID)
	if err != nil {
		return err
	}
	if keyRow == nil {
		return nil
	}
	return w.sweepDeposit(ctx, p, keyRow)
}

// sweepDeposit sends the deposit balance to the merchant wallet and, on a
// later tick once the sweep finalizes, marks the payment finalized.
func (w *Watcher) sweepDeposit(ctx context.Context, p *store.Payment, keyRow *store.DepositKeyRow) error {
	if w.Sweeper == nil {
		w.Log.Warn("deposit payment confirmed but no sweeper configured",
			"payment_id", p.ID, "reference", p.Reference)
		return nil
	}
	if p.SweepTx != "" {
		done, err := w.Sweeper.Finalized(ctx, p.SweepTx)
		if err != nil {
			return err
		}
		if !done {
			return nil
		}
		if err := w.DB.FinalizePayment(ctx, p.ID, w.now()); err != nil {
			return err
		}
		w.transition("confirmed", "finalized", "")
		if err := w.DB.EnqueueWebhook(ctx, p.ID, "payment.finalized"); err != nil {
			return err
		}
		w.Log.Info("deposit payment finalized (swept)", "payment_id", p.ID,
			"reference", p.Reference, "sweep_tx", p.SweepTx)
		return nil
	}
	secret, err := wallet.DecryptSecret(keyRow.EncryptedSecret, w.MasterKey)
	if err != nil {
		return err
	}
	sig, err := w.Sweeper.Sweep(ctx, secret)
	if err != nil {
		w.Log.Error("sweep failed", "payment_id", p.ID, "reference", p.Reference, "err", err.Error())
		return nil // retried next tick
	}
	if err := w.DB.SetSweepTx(ctx, p.ID, sig); err != nil {
		return err
	}
	w.Log.Info("deposit swept to merchant", "payment_id", p.ID,
		"reference", p.Reference, "sweep_tx", sig)
	return nil
}

// SweepLateDeposit handles an expired deposit payment whose funds arrived
// after expiry: custodial mode must still deliver the money. Re-confirming
// re-enters the normal sweep path.
func (w *Watcher) SweepLateDeposit(ctx context.Context, p *store.Payment) error {
	keyRow, err := w.DB.GetDepositKey(ctx, p.ID)
	if err != nil || keyRow == nil {
		return err
	}
	balancer, ok := w.Detector.(DepositBalanceReader)
	if !ok {
		return nil
	}
	balance, err := balancer.GetBalance(ctx, p.Reference)
	if err != nil {
		return err
	}
	if balance == 0 {
		return nil
	}
	if err := w.DB.MarkDetectedConfirmed(ctx, p.ID, p.TxSignature, time.Now().UTC()); err != nil {
		return err
	}
	w.transition("expired", "confirmed", "")
	if err := w.DB.EnqueueWebhook(ctx, p.ID, "payment.confirmed"); err != nil {
		return err
	}
	w.Log.Warn("late funds on expired deposit payment; sweeping",
		"payment_id", p.ID, "reference", p.Reference, "balance", balance)
	return w.sweepDeposit(ctx, &store.Payment{ID: p.ID, Reference: p.Reference, Status: "confirmed"}, keyRow)
}

// DepositBalanceReader is the optional balance capability of a Detector.
type DepositBalanceReader interface {
	GetBalance(ctx context.Context, address string) (uint64, error)
}
