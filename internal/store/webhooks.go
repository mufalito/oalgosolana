package store

import (
	"context"
	"time"
)

// DeliveryWithURL is a claimed delivery joined with its target URL.
type DeliveryWithURL struct {
	WebhookDelivery
	URL string
}

// ClaimDueDeliveries claims pending deliveries whose next_attempt_at is due,
// using FOR UPDATE SKIP LOCKED so replicas never double-claim.
func (s *Store) ClaimDueDeliveries(ctx context.Context, limit int) ([]DeliveryWithURL, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT d.id, d.payment_id, d.event_type, d.payload, d.status, d.attempts,
		       d.next_attempt_at, d.last_error, d.sent_at, p.webhook_url
		FROM webhook_deliveries d
		JOIN payments p ON p.id = d.payment_id
		WHERE d.status = 'pending' AND d.next_attempt_at <= now()
		ORDER BY d.id
		LIMIT $1
		FOR UPDATE OF d SKIP LOCKED`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeliveryWithURL
	for rows.Next() {
		var d DeliveryWithURL
		if err := rows.Scan(&d.ID, &d.PaymentID, &d.EventType, &d.Payload, &d.Status,
			&d.Attempts, &d.NextAttemptAt, &d.LastError, &d.SentAt, &d.URL); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// MarkDeliverySent records a successful delivery.
func (s *Store) MarkDeliverySent(ctx context.Context, id int64, at time.Time) error {
	_, err := s.Pool.Exec(ctx,
		`UPDATE webhook_deliveries SET status='sent', sent_at=$2, last_error='' WHERE id=$1`, id, at)
	return err
}

// ScheduleRetry records a failed attempt and the next due time.
func (s *Store) ScheduleRetry(ctx context.Context, id int64, attempts int, lastErr string, next time.Time) error {
	_, err := s.Pool.Exec(ctx,
		`UPDATE webhook_deliveries SET attempts=$2, last_error=$3, next_attempt_at=$4 WHERE id=$1`,
		id, attempts, lastErr, next)
	return err
}

// MarkDeliveryDead dead-letters a delivery after exhausting attempts.
func (s *Store) MarkDeliveryDead(ctx context.Context, id int64, attempts int, lastErr string) error {
	_, err := s.Pool.Exec(ctx,
		`UPDATE webhook_deliveries SET status='dead', attempts=$2, last_error=$3 WHERE id=$1`,
		id, attempts, lastErr)
	return err
}

// ReplayDelivery resets every dead or pending delivery for a payment.
// Returns found=false when the payment does not exist.
func (s *Store) ReplayDelivery(ctx context.Context, paymentID string) (found bool, err error) {
	var exists bool
	if err := s.Pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM payments WHERE id=$1)`, paymentID).Scan(&exists); err != nil {
		return false, err
	}
	if !exists {
		return false, nil
	}
	_, err = s.Pool.Exec(ctx, `
		UPDATE webhook_deliveries
		SET status='pending', attempts=0, next_attempt_at=now(), last_error=''
		WHERE payment_id=$1 AND status IN ('dead','pending')`, paymentID)
	return true, err
}

// WebhookStatuses maps event type -> delivery status for a payment.
func (s *Store) WebhookStatuses(ctx context.Context, paymentID string) (map[string]string, error) {
	rows, err := s.Pool.Query(ctx,
		`SELECT event_type, status FROM webhook_deliveries WHERE payment_id=$1`, paymentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var ev, st string
		if err := rows.Scan(&ev, &st); err != nil {
			return nil, err
		}
		out[ev] = st
	}
	return out, rows.Err()
}
