package webhook

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/jackc/pgx/v5"

	"github.com/mufalito/oalgosolana/internal/store"
)

func testURL(t *testing.T) string {
	t.Helper()
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		base = "postgres://solanapay:solanapay@localhost:5432/postgres?sslmode=disable"
	}
	name := "solanapay_test_webhook"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Skipf("database not reachable: %v", err)
	}
	defer c.Close(context.Background())
	_, _ = c.Exec(ctx, "CREATE DATABASE "+name)
	return strings.Replace(base, "/postgres?", "/"+name+"?", 1)
}

func testDB(t *testing.T) *store.Store {
	t.Helper()
	url := testURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, err := store.Open(ctx, url)
	if err != nil {
		t.Skipf("database not reachable: %v", err)
	}
	t.Cleanup(st.Close)
	if _, err := st.Pool.Exec(ctx, `TRUNCATE webhook_deliveries, payment_txs, deposit_keys, payments`); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestSignatureDeterministicAndVerifiable(t *testing.T) {
	s := &Signer{Secret: []byte("0123456789abcdef0123456789abcdef")}
	ts := time.Unix(1700000000, 0)
	sig1, tsH := s.Headers([]byte(`{"a":1}`), ts)
	sig2, _ := s.Headers([]byte(`{"a":1}`), ts)
	if sig1 != sig2 {
		t.Fatal("signature not deterministic")
	}
	if !strings.HasPrefix(sig1, "sha256=") {
		t.Fatal("signature header prefix")
	}
	if err := s.Verify(sig1, tsH, []byte(`{"a":1}`), ts, 5*time.Minute); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if err := s.Verify(sig1, tsH, []byte(`{"a":2}`), ts, 5*time.Minute); err == nil {
		t.Fatal("tampered body must fail")
	}
	if err := s.Verify("sha256=deadbeef", tsH, []byte(`{"a":1}`), ts, 5*time.Minute); err == nil {
		t.Fatal("wrong signature must fail")
	}
	// skew > 5 min rejected
	if err := s.Verify(sig1, tsH, []byte(`{"a":1}`), ts.Add(6*time.Minute), 5*time.Minute); err == nil {
		t.Fatal("skew must fail")
	}
	if err := s.Verify(sig1, tsH, []byte(`{"a":1}`), ts.Add(-6*time.Minute), 5*time.Minute); err == nil {
		t.Fatal("negative skew must fail")
	}
}

func TestBackoffSchedule(t *testing.T) {
	d := &Delivery{MaxAttempts: 8}
	d.ensureRng()
	// backoff bounds: [0.5*2^n, 1.0*2^n] seconds, capped at 10m
	for attempt := 1; attempt <= 7; attempt++ {
		for range 20 {
			got := d.nextBackoff(attempt)
			lower := time.Duration(float64(time.Duration(1<<attempt)*time.Second) * 0.49)
			upper := time.Duration(float64(time.Duration(1<<attempt)*time.Second) * 1.01)
			capLimit := 10 * time.Minute
			_ = capLimit
			if upper > capLimit {
				upper = capLimit
			}
			if got < lower || got > upper {
				t.Fatalf("attempt %d: backoff %v outside [%v,%v]", attempt, got, lower, upper)
			}
		}
	}
}

func seedPayment(t *testing.T, db *store.Store) *store.Payment {
	t.Helper()
	p := &store.Payment{
		ID: uuid.Must(uuid.NewV7()).String(), OrderID: "wh_" + uuid.NewString()[:8],
		AmountBase: 1500000000, Currency: "SOL", Status: "confirmed",
		Reference:      "E1GaxPQ4xr6PWT2BPY5tLGfLnwsNaNdABqknaGfcC5AF",
		MerchantWallet: "9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM",
		WebhookURL:     "https://example.invalid/hook",
		ExpiresAt:      time.Now().Add(time.Hour), CreatedAt: time.Now(),
		TxSignature: "sig123",
	}
	if err := db.CreatePayment(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDeliveryHMACVerifiedByReceiver(t *testing.T) {
	db := testDB(t)
	p := seedPayment(t, db)
	ctx := context.Background()
	if err := db.EnqueueWebhook(ctx, p.ID, "payment.confirmed"); err != nil {
		t.Fatal(err)
	}

	secret := []byte("0123456789abcdef0123456789abcdef")
	signer := &Signer{Secret: secret}

	var mu atomic.Int32
	rcv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := signer.Verify(r.Header.Get("X-Signature-256"), r.Header.Get("X-Webhook-Timestamp"), body, time.Now(), 5*time.Minute); err != nil {
			t.Errorf("receiver HMAC verification failed: %v", err)
			w.WriteHeader(400)
			return
		}
		var pl Payload
		json.Unmarshal(body, &pl)
		if pl.Type != "payment.confirmed" || pl.PaymentID != p.ID || pl.AmountBase != "1500000000" {
			t.Errorf("unexpected payload: %+v", pl)
		}
		mu.Add(1)
		w.WriteHeader(200)
	}))
	defer rcv.Close()
	// point delivery at the test receiver
	db.Pool.Exec(ctx, `UPDATE payments SET webhook_url=$2 WHERE id=$1`, p.ID, rcv.URL)

	d := &Delivery{DB: db, Signer: signer, MaxAttempts: 3, Log: slog.New(slog.NewTextHandler(os.Stderr, nil))}
	if err := d.deliverDue(ctx); err != nil {
		t.Fatal(err)
	}
	var status string
	db.Pool.QueryRow(ctx, `SELECT status FROM webhook_deliveries WHERE payment_id=$1`, p.ID).Scan(&status)
	if status != "sent" {
		t.Fatalf("delivery status = %s", status)
	}
	if mu.Load() != 1 {
		t.Fatalf("receiver got %d deliveries", mu.Load())
	}
}

func TestRetryThenDeadThenReplay(t *testing.T) {
	db := testDB(t)
	p := seedPayment(t, db)
	ctx := context.Background()
	if err := db.EnqueueWebhook(ctx, p.ID, "payment.confirmed"); err != nil {
		t.Fatal(err)
	}

	var calls atomic.Int32
	rcv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(500) // always fail
	}))
	defer rcv.Close()
	db.Pool.Exec(ctx, `UPDATE payments SET webhook_url=$2 WHERE id=$1`, p.ID, rcv.URL)

	d := &Delivery{
		DB: db, Signer: &Signer{Secret: []byte("0123456789abcdef0123456789abcdef")},
		MaxAttempts: 2, Log: slog.New(slog.NewTextHandler(os.Stderr, nil)),
		Backoff: func(int, *rand.Rand) time.Duration { return 0 },
	}

	d.deliverDue(ctx) // attempt 1 -> retry
	var status string
	var attempts int
	db.Pool.QueryRow(ctx, `SELECT status, attempts FROM webhook_deliveries WHERE payment_id=$1`, p.ID).Scan(&status, &attempts)
	if status != "pending" || attempts != 1 {
		t.Fatalf("after 1st: status=%s attempts=%d", status, attempts)
	}
	db.Pool.Exec(ctx, `UPDATE webhook_deliveries SET next_attempt_at=now() WHERE payment_id=$1`, p.ID)
	d.deliverDue(ctx) // attempt 2 -> dead
	db.Pool.QueryRow(ctx, `SELECT status, attempts FROM webhook_deliveries WHERE payment_id=$1`, p.ID).Scan(&status, &attempts)
	if status != "dead" || attempts != 2 {
		t.Fatalf("after 2nd: status=%s attempts=%d", status, attempts)
	}

	// replay resets
	found, err := db.ReplayDelivery(ctx, p.ID)
	if err != nil || !found {
		t.Fatalf("replay: found=%v err=%v", found, err)
	}
	db.Pool.QueryRow(ctx, `SELECT status, attempts FROM webhook_deliveries WHERE payment_id=$1`, p.ID).Scan(&status, &attempts)
	if status != "pending" || attempts != 0 {
		t.Fatalf("after replay: status=%s attempts=%d", status, attempts)
	}

	// unknown payment -> not found
	found, err = db.ReplayDelivery(ctx, uuid.Must(uuid.NewV7()).String())
	if err != nil || found {
		t.Fatalf("replay unknown: found=%v err=%v", found, err)
	}
}
