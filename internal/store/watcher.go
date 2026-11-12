package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// PaymentCols is the canonical column list for scanning payment rows.
const PaymentCols = `id, order_id, amount_base, currency, status, reference, merchant_wallet,
label, message, webhook_url, expires_at, created_at, tx_signature, detected_tx, sweep_tx,
confirmed_at, finalized_at, failure_reason`

// Querier abstracts pgx tx + pool so transitions can run inside a tx.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// WithTx runs fn inside a single database transaction.
func (s *Store) WithTx(ctx context.Context, fn func(q Querier) error) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback(ctx)
		return err
	}
	return tx.Commit(ctx)
}

// ClaimSignature inserts a (payment_id, tx_signature) idempotency row and
// reports whether this call was the first to claim it.
func (s *Store) ClaimSignature(ctx context.Context, paymentID, sig string) (bool, error) {
	tag, err := s.Pool.Exec(ctx,
		`INSERT INTO payment_txs (payment_id, tx_signature) VALUES ($1,$2) ON CONFLICT DO NOTHING`,
		paymentID, sig)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// HasPaymentTx reports whether the signature was already recorded.
func (s *Store) HasPaymentTx(ctx context.Context, paymentID, sig string) (bool, error) {
	var exists bool
	err := s.Pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM payment_txs WHERE payment_id=$1 AND tx_signature=$2)`,
		paymentID, sig).Scan(&exists)
	return exists, err
}

// MarkProcessing transitions a payment with a claimed candidate to processing.
func (s *Store) MarkProcessing(ctx context.Context, paymentID, sig string) error {
	_, err := s.Pool.Exec(ctx,
		`UPDATE payments SET status='processing', tx_signature=$2 WHERE id=$1 AND status='pending'`,
		paymentID, sig)
	return err
}

// ConfirmPayment sets status confirmed and records the signature + time.
func (s *Store) ConfirmPayment(ctx context.Context, paymentID, sig string, at time.Time) error {
	_, err := s.Pool.Exec(ctx,
		`UPDATE payments SET status='confirmed', tx_signature=$2, confirmed_at=$3,
		   failure_reason='' WHERE id=$1 AND status IN ('processing','pending')`,
		paymentID, sig, at)
	return err
}

// FinalizePayment escalates a confirmed payment to finalized.
func (s *Store) FinalizePayment(ctx context.Context, paymentID string, at time.Time) error {
	_, err := s.Pool.Exec(ctx,
		`UPDATE payments SET status='finalized', finalized_at=$2 WHERE id=$1 AND status IN ('confirmed','processing','pending')`,
		paymentID, at)
	return err
}

// FailPayment marks a payment failed with an exact reason code.
func (s *Store) FailPayment(ctx context.Context, paymentID, reason string, at time.Time) error {
	_, err := s.Pool.Exec(ctx,
		`UPDATE payments SET status='failed', failure_reason=$2 WHERE id=$1 AND status IN ('pending','processing','confirmed')`,
		paymentID, reason)
	return err
}

// EnqueueWebhook queues an event delivery for the payment's webhook_url.
// It is a no-op when the payment has no webhook configured, and never
// duplicates an event (unique payment_id+event_type).
func (s *Store) EnqueueWebhook(ctx context.Context, paymentID, eventType string) error {
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO webhook_deliveries (payment_id, event_type, payload, status)
		SELECT p.id, $2::text, jsonb_build_object('payment_id', p.id::text, 'event_type', $2::text), 'pending'
		FROM payments p
		WHERE p.id = $1 AND p.webhook_url <> ''
		ON CONFLICT (payment_id, event_type) DO NOTHING`,
		paymentID, eventType)
	return err
}

// MarkDetectedTx records a late-but-valid transaction on an expired payment.
func (s *Store) MarkDetectedTx(ctx context.Context, paymentID, sig string) error {
	_, err := s.Pool.Exec(ctx,
		`UPDATE payments SET detected_tx=$2 WHERE id=$1 AND status='expired'`,
		paymentID, sig)
	return err
}
