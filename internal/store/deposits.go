package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// DepositKeyRow is the persisted deposit keypair (secret still encrypted).
type DepositKeyRow struct {
	PaymentID       string
	PublicKey       string
	EncryptedSecret []byte
}

// CreateDepositKey stores the encrypted deposit secret for a payment.
func (s *Store) CreateDepositKey(ctx context.Context, r *DepositKeyRow) error {
	_, err := s.Pool.Exec(ctx,
		`INSERT INTO deposit_keys (payment_id, public_key, encrypted_secret) VALUES ($1,$2,$3)`,
		r.PaymentID, r.PublicKey, r.EncryptedSecret)
	return err
}

// GetDepositKey fetches the deposit key row for a payment; nil if none.
func (s *Store) GetDepositKey(ctx context.Context, paymentID string) (*DepositKeyRow, error) {
	var r DepositKeyRow
	err := s.Pool.QueryRow(ctx,
		`SELECT payment_id, public_key, encrypted_secret FROM deposit_keys WHERE payment_id=$1`,
		paymentID).Scan(&r.PaymentID, &r.PublicKey, &r.EncryptedSecret)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// HasDepositKey reports whether the payment is deposit-address mode.
func (s *Store) HasDepositKey(ctx context.Context, paymentID string) (bool, error) {
	r, err := s.GetDepositKey(ctx, paymentID)
	return r != nil, err
}

// SetSweepTx records the sweep transaction signature.
func (s *Store) SetSweepTx(ctx context.Context, paymentID, sweepTx string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE payments SET sweep_tx=$2 WHERE id=$1`, paymentID, sweepTx)
	return err
}

// MarkDetectedConfirmed transitions an expired deposit payment whose funds
// arrived late straight to confirmed (custodial mode must still sweep).
func (s *Store) MarkDetectedConfirmed(ctx context.Context, paymentID, sig string, at time.Time) error {
	_, err := s.Pool.Exec(ctx,
		`UPDATE payments SET status='confirmed', tx_signature=$2, confirmed_at=$3, failure_reason=''
		 WHERE id=$1 AND status='expired'`, paymentID, sig, at)
	return err
}
