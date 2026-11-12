package store

import (
	"context"
	"time"
)

// ConfirmDeposit marks a deposit payment confirmed and records both the
// incoming payment signature (receipt, may be empty) and the sweep tx.
func (s *Store) ConfirmDeposit(ctx context.Context, paymentID, incomingSig, sweepTx string, at time.Time) error {
	_, err := s.Pool.Exec(ctx,
		`UPDATE payments SET status='confirmed', tx_signature=$2, sweep_tx=$3, confirmed_at=$4,
		   failure_reason=''
		 WHERE id=$1 AND status IN ('pending','processing','confirmed','expired')`,
		paymentID, incomingSig, sweepTx, at)
	return err
}

// SetSweepTx records/updates the sweep transaction signature (retry path).
func (s *Store) SetSweepTx(ctx context.Context, paymentID, sweepTx string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE payments SET sweep_tx=$2 WHERE id=$1`, paymentID, sweepTx)
	return err
}
