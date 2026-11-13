package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ErrNotFound is returned when a payment does not exist.
var ErrNotFound = errors.New("payment not found")

const paymentCols = `id, order_id, amount_base, decimals, currency, status, reference, merchant_wallet,
label, message, webhook_url, expires_at, created_at, tx_signature, detected_tx, sweep_tx,
confirmed_at, finalized_at, failure_reason`

func scanPayment(row pgx.Row) (*Payment, error) {
	var p Payment
	err := row.Scan(&p.ID, &p.OrderID, &p.AmountBase, &p.Decimals, &p.Currency, &p.Status, &p.Reference,
		&p.MerchantWallet, &p.Label, &p.Message, &p.WebhookURL, &p.ExpiresAt, &p.CreatedAt,
		&p.TxSignature, &p.DetectedTx, &p.SweepTx, &p.ConfirmedAt, &p.FinalizedAt, &p.FailureReason)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// CreatePayment inserts a new payment row.
func (s *Store) CreatePayment(ctx context.Context, p *Payment) error {
	_, err := s.Pool.Exec(ctx,
		`INSERT INTO payments (id, order_id, amount_base, decimals, currency, status, reference,
		   merchant_wallet, label, message, webhook_url, expires_at, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		p.ID, p.OrderID, p.AmountBase, p.Decimals, p.Currency, p.Status, p.Reference,
		p.MerchantWallet, p.Label, p.Message, p.WebhookURL, p.ExpiresAt, p.CreatedAt)
	return err
}

// GetPayment fetches one payment by id.
func (s *Store) GetPayment(ctx context.Context, id uuid.UUID) (*Payment, error) {
	row := s.Pool.QueryRow(ctx, `SELECT `+paymentCols+` FROM payments WHERE id = $1`, id)
	return scanPayment(row)
}

// PaymentsRaw runs an arbitrary row-returning query (watcher claim path).
func (s *Store) QueryPayments(ctx context.Context, sql string, args ...any) ([]*Payment, error) {
	rows, err := s.Pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Payment
	for rows.Next() {
		p, err := scanPayment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ExpirePending marks overdue pending/processing payments expired; returns the
// ids transitioned so the caller can enqueue payment.expired webhooks.
func (s *Store) ExpirePending(ctx context.Context, now time.Time) ([]string, error) {
	rows, err := s.Pool.Query(ctx,
		`UPDATE payments SET status = 'expired'
		 WHERE status IN ('pending','processing') AND expires_at <= $1
		 RETURNING id`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
