// Package webhook delivers signed event payloads to merchant endpoints with
// HMAC-SHA256 signatures, exponential backoff + jitter, retries, dead-letter
// state, and manual replay.
package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"

	"github.com/mufalito/oalgosolana/internal/solanapay"

	"github.com/google/uuid"
	"github.com/mufalito/oalgosolana/internal/store"
)

// Signer produces webhook signature headers.
type Signer struct {
	Secret []byte
}

// Headers returns X-Signature-256 (sha256=<hex>) and X-Webhook-Timestamp
// for the given raw body. The MAC covers timestamp + "." + body.
func (s *Signer) Headers(body []byte, ts time.Time) (sig, tsHeader string) {
	tsHeader = strconv.FormatInt(ts.Unix(), 10)
	mac := hmac.New(sha256.New, s.Secret)
	mac.Write([]byte(tsHeader))
	mac.Write([]byte("."))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil)), tsHeader
}

// Verify checks a signature header against timestamp + "." + body using
// constant-time comparison. Skew > maxSkew is rejected (replay protection).
func (s *Signer) Verify(sigHeader, tsHeader string, body []byte, now time.Time, maxSkew time.Duration) error {
	ts, err := strconv.ParseInt(tsHeader, 10, 64)
	if err != nil {
		return fmt.Errorf("bad timestamp header")
	}
	if d := now.Sub(time.Unix(ts, 0)); d > maxSkew || d < -maxSkew {
		return fmt.Errorf("timestamp skew %v exceeds %v", d, maxSkew)
	}
	mac := hmac.New(sha256.New, s.Secret)
	mac.Write([]byte(tsHeader))
	mac.Write([]byte("."))
	mac.Write(body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(want), []byte(sigHeader)) != 1 {
		return fmt.Errorf("signature mismatch")
	}
	return nil
}

// Payload is the event body POSTed to merchants.
type Payload struct {
	Type        string `json:"type"`
	PaymentID   string `json:"payment_id"`
	OrderID     string `json:"order_id"`
	Amount      string `json:"amount"`
	AmountBase  string `json:"amount_base"`
	Currency    string `json:"currency"`
	Reference   string `json:"reference"`
	TxSignature string `json:"tx_signature,omitempty"`
	Status      string `json:"status"`
	ConfirmedAt string `json:"confirmed_at,omitempty"`
	FinalizedAt string `json:"finalized_at,omitempty"`
	ExpiresAt   string `json:"expires_at,omitempty"`
}

// BuildPayload assembles the event body for a payment row.
func BuildPayload(eventType string, p *store.Payment) Payload {
	pl := Payload{
		Type:        eventType,
		PaymentID:   p.ID,
		OrderID:     p.OrderID,
		Amount:      solanapay.FormatAmount(p.AmountBase),
		AmountBase:  strconv.FormatUint(p.AmountBase, 10),
		Currency:    p.Currency,
		TxSignature: p.TxSignature,
		ExpiresAt:   p.ExpiresAt.Format(time.RFC3339),
	}
	switch eventType {
	case "payment.confirmed":
		pl.Status = "confirmed"
		if p.ConfirmedAt != nil {
			pl.ConfirmedAt = p.ConfirmedAt.Format(time.RFC3339)
		}
	case "payment.finalized":
		pl.Status = "finalized"
		if p.ConfirmedAt != nil {
			pl.ConfirmedAt = p.ConfirmedAt.Format(time.RFC3339)
		}
		if p.FinalizedAt != nil {
			pl.FinalizedAt = p.FinalizedAt.Format(time.RFC3339)
		}
	case "payment.expired":
		pl.Status = "expired"
	}
	return pl
}

// Delivery claims due webhook_deliveries rows and POSTs them.
type Delivery struct {
	DB          *store.Store
	Signer      *Signer
	Log         *slog.Logger
	MaxAttempts int
	// OnAttempt is a metrics hook (result: sent|retry|dead).
	OnAttempt func(result string, duration time.Duration)
	// Backoff computes the next-attempt delay after attempt n (1-based).
	Backoff func(attempt int, r *rand.Rand) time.Duration
	Client  *http.Client
	rng     *rand.Rand
}

// ensureRng lazily initializes the RNG.
func (d *Delivery) ensureRng() {
	if d.rng == nil {
		d.rng = rand.New(rand.NewPCG(1, 1))
	}
}

// Run polls for due deliveries until ctx is cancelled.
func (d *Delivery) Run(ctx context.Context) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := d.deliverDue(ctx); err != nil && !errors.Is(err, context.Canceled) {
				d.Log.Error("webhook poll failed", "err", err.Error())
			}
		}
	}
}

// deliverDue claims one batch of due pending deliveries.
func (d *Delivery) deliverDue(ctx context.Context) error {
	rows, err := d.DB.ClaimDueDeliveries(ctx, 10)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if err := d.attempt(ctx, row); err != nil {
			d.Log.Error("webhook attempt crashed", "delivery_id", row.ID, "err", err.Error())
		}
	}
	return nil
}

// attempt POSTs one delivery and records sent / retry / dead.
func (d *Delivery) attempt(ctx context.Context, row store.DeliveryWithURL) error {
	payment, err := d.DB.GetPayment(ctx, uuid.Must(uuid.Parse(row.PaymentID)))
	if err != nil {
		return err
	}
	payload := BuildPayload(row.EventType, payment)
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	sig, tsHeader := d.Signer.Headers(body, time.Now())

	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, row.URL, bytes.NewReader(body))
	if err != nil {
		return d.fail(ctx, row, err, start)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Signature-256", sig)
	req.Header.Set("X-Webhook-Timestamp", tsHeader)
	req.Header.Set("X-Webhook-Event", row.EventType)
	client := d.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return d.fail(ctx, row, err, start)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if err := d.DB.MarkDeliverySent(ctx, row.ID, start); err != nil {
			return err
		}
		if d.OnAttempt != nil {
			d.OnAttempt("sent", time.Since(start))
		}
		d.Log.Info("webhook delivered", "delivery_id", row.ID, "payment_id", row.PaymentID,
			"event", row.EventType, "status_code", resp.StatusCode)
		return nil
	}
	return d.fail(ctx, row, fmt.Errorf("http %d", resp.StatusCode), start)
}

// fail records a failed attempt: retry with backoff or dead-letter.
func (d *Delivery) fail(ctx context.Context, row store.DeliveryWithURL, cause error, start time.Time) error {
	attempts := row.Attempts + 1
	if attempts >= d.MaxAttempts {
		if err := d.DB.MarkDeliveryDead(ctx, row.ID, attempts, cause.Error()); err != nil {
			return err
		}
		if d.OnAttempt != nil {
			d.OnAttempt("dead", time.Since(start))
		}
		d.Log.Warn("webhook dead-lettered", "delivery_id", row.ID, "payment_id", row.PaymentID,
			"attempts", attempts, "err", cause.Error())
		return nil
	}
	d.ensureRng()
	backoff := d.nextBackoff(attempts)
	if err := d.DB.ScheduleRetry(ctx, row.ID, attempts, cause.Error(), time.Now().Add(backoff)); err != nil {
		return err
	}
	if d.OnAttempt != nil {
		d.OnAttempt("retry", time.Since(start))
	}
	d.Log.Warn("webhook attempt failed", "delivery_id", row.ID, "payment_id", row.PaymentID,
		"attempt", attempts, "retry_in", backoff.String(), "err", cause.Error())
	return nil
}

// nextBackoff: 2^attempt seconds * (0.5 + jitter), capped at 10 minutes.
func (d *Delivery) nextBackoff(attempt int) time.Duration {
	if d.Backoff != nil {
		return d.Backoff(attempt, d.rng)
	}
	base := time.Duration(1<<uint(min(attempt, 20))) * time.Second
	dur := time.Duration(float64(base) * (0.5 + d.rng.Float64()*0.5))
	if dur > 10*time.Minute {
		dur = 10 * time.Minute
	}
	return dur
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
