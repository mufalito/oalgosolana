package watcher

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mufalito/oalgosolana/internal/chain"
	"github.com/mufalito/oalgosolana/internal/store"
	"github.com/mufalito/oalgosolana/internal/wallet"
)

// fakeSweeper records sweep calls.
type fakeSweeper struct {
	swept     []string // deposit pubkeys swept
	sweepSig  string
	finalized bool
	calls     int
}

func (f *fakeSweeper) Sweep(_ context.Context, secret []byte) (string, error) {
	if len(secret) != 32 {
		return "", fmt.Errorf("secret must be 32-byte seed, got %d", len(secret))
	}
	f.swept = append(f.swept, base58ish(secret))
	f.calls++
	return "sweep_sig_" + base58ish(secret), nil
}

func base58ish(b []byte) string { return hex.EncodeToString(b)[:16] }

func (f *fakeSweeper) Finalized(_ context.Context, sig string) (bool, error) {
	return sig == f.sweepSig && f.finalized, nil
}

// depositDetector reports one incoming transfer of fullAmount to the deposit
// address and serves the balance reader interface.
type depositDetector struct {
	fakeDetector
	depositAddr string
	fullAmount  uint64
	balance     uint64
}

func (d *depositDetector) SignaturesForAddress(_ context.Context, ref string) ([]string, error) {
	if ref == d.depositAddr {
		return []string{"incoming_sig_1"}, nil
	}
	return nil, nil
}

func (d *depositDetector) Transaction(_ context.Context, sig, recipient, _ string) (*chain.TxInfo, error) {
	if sig != "incoming_sig_1" {
		return nil, &chain.NotFoundError{Method: "getTransaction"}
	}
	return &chain.TxInfo{
		Signature: sig, RecipientDeltaLamports: d.fullAmount,
		TokenDeltas: map[string]uint64{},
		AccountKeys: []string{"SomeSender11111111111111111111111111111111111", recipient},
	}, nil
}

func (d *depositDetector) GetBalance(_ context.Context, addr string) (uint64, error) {
	if addr == d.depositAddr {
		return d.balance, nil
	}
	return 0, nil
}

func TestDepositFlowEndToEnd(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	masterKey := make([]byte, 32)
	for i := range masterKey {
		masterKey[i] = byte(i)
	}
	seed := make([]byte, 32)
	for i := range seed {
		seed[i] = byte(255 - i)
	}
	enc, err := wallet.EncryptSecret(seed, masterKey)
	if err != nil {
		t.Fatal(err)
	}

	p := &store.Payment{
		ID: uuid.Must(uuid.NewV7()).String(), OrderID: "dep_" + uuid.NewString()[:8],
		AmountBase: 50000000, Currency: "SOL", Status: "pending",
		Reference: vaultRef(t, enc, masterKey), MerchantWallet: testMerchant,
		WebhookURL: "https://example.com/hook",
		ExpiresAt:  time.Now().Add(30 * time.Minute), CreatedAt: time.Now(),
	}
	if err := db.CreatePayment(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateDepositKey(ctx, &store.DepositKeyRow{
		PaymentID: p.ID, PublicKey: p.Reference, EncryptedSecret: enc,
	}); err != nil {
		t.Fatal(err)
	}

	swp := &fakeSweeper{finalized: false}
	det := &depositDetector{depositAddr: rowRef(t, db, p.ID), fullAmount: 50000000}
	w := &Watcher{
		DB: db, Detector: det, Log: logNop(t),
		Sweeper: swp, MasterKey: masterKey,
		Cfg: WatcherConfig{ConfirmationPolicy: "confirmed", PollInterval: time.Millisecond, Workers: 2},
	}

	// tick 1: pending -> processing -> confirmed (full amount arrived) + sweep sent
	if err := w.pollOnce(ctx); err != nil {
		t.Fatal(err)
	}
	row, _ := db.GetPayment(ctx, uuid.MustParse(p.ID))
	if row.Status != "confirmed" {
		t.Fatalf("after tick1: %s", row.Status)
	}
	if len(swp.swept) != 1 {
		t.Fatalf("swept: %v", swp.swept)
	}
	if row.SweepTx == "" {
		t.Fatal("sweep tx not recorded")
	}
	swp.sweepSig = row.SweepTx

	// tick 2: sweep not yet finalized -> stays confirmed
	if err := w.pollOnce(ctx); err != nil {
		t.Fatal(err)
	}
	row, _ = db.GetPayment(ctx, uuid.MustParse(p.ID))
	if row.Status != "confirmed" {
		t.Fatalf("after tick2: %s", row.Status)
	}

	// tick 3: sweep finalized -> payment finalized + webhooks
	swp.finalized = true
	if err := w.pollOnce(ctx); err != nil {
		t.Fatal(err)
	}
	row, _ = db.GetPayment(ctx, uuid.MustParse(p.ID))
	if row.Status != "finalized" || row.FinalizedAt == nil {
		t.Fatalf("after tick3: %s", row.Status)
	}
	var n int
	db.Pool.QueryRow(ctx, `SELECT count(*) FROM webhook_deliveries WHERE payment_id=$1`, p.ID).Scan(&n)
	if n != 2 {
		t.Fatalf("deliveries = %d, want 2", n)
	}
}

func TestDepositPartialPaymentKeepsWatching(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	masterKey := make([]byte, 32)
	seed := make([]byte, 32)
	enc, _ := wallet.EncryptSecret(seed, masterKey)
	_ = seed
	p := &store.Payment{
		ID: uuid.Must(uuid.NewV7()).String(), OrderID: "dep_" + uuid.NewString()[:8],
		AmountBase: 50000000, Currency: "SOL", Status: "pending",
		Reference: vaultRef(t, enc, masterKey), MerchantWallet: testMerchant,
		ExpiresAt: time.Now().Add(30 * time.Minute), CreatedAt: time.Now(),
	}
	db.CreatePayment(ctx, p)
	db.CreateDepositKey(ctx, &store.DepositKeyRow{
		PaymentID: p.ID, PublicKey: p.Reference, EncryptedSecret: enc,
	})

	swp := &fakeSweeper{}
	det := &depositDetector{depositAddr: rowRef(t, db, p.ID), fullAmount: 10000000} // 20% paid
	w := &Watcher{DB: db, Detector: det, Log: logNop(t), Sweeper: swp, MasterKey: masterKey,
		Cfg: WatcherConfig{ConfirmationPolicy: "confirmed", PollInterval: time.Millisecond, Workers: 2}}

	for range 3 {
		if err := w.pollOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	row, _ := db.GetPayment(ctx, uuid.MustParse(p.ID))
	if row.Status != "processing" {
		t.Fatalf("partial payment must stay processing, got %s", row.Status)
	}
	if len(swp.swept) != 0 {
		t.Fatal("must not sweep unconfirmed/partial payment")
	}
}

func TestSweepLateDepositAfterExpiry(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	masterKey := make([]byte, 32)
	seed := make([]byte, 32)
	enc, _ := wallet.EncryptSecret(seed, masterKey)
	_ = seed
	p := &store.Payment{
		ID: uuid.Must(uuid.NewV7()).String(), OrderID: "dep_" + uuid.NewString()[:8],
		AmountBase: 50000000, Currency: "SOL", Status: "expired",
		Reference: vaultRef(t, enc, masterKey), MerchantWallet: testMerchant,
		ExpiresAt: time.Now().Add(-time.Minute), CreatedAt: time.Now(),
	}
	db.CreatePayment(ctx, p)
	db.CreateDepositKey(ctx, &store.DepositKeyRow{
		PaymentID: p.ID, PublicKey: p.Reference, EncryptedSecret: enc,
	})

	swp := &fakeSweeper{}
	det := &depositDetector{depositAddr: rowRef(t, db, p.ID), fullAmount: 50000000, balance: 50000000}
	w := &Watcher{DB: db, Detector: det, Log: logNop(t), Sweeper: swp, MasterKey: masterKey,
		Cfg: WatcherConfig{ConfirmationPolicy: "confirmed", PollInterval: time.Millisecond, Workers: 2}}

	if err := w.SweepLateDeposit(ctx, p); err != nil {
		t.Fatal(err)
	}
	row, _ := db.GetPayment(ctx, uuid.MustParse(p.ID))
	if row.Status != "confirmed" {
		t.Fatalf("late deposit must be re-confirmed, got %s", row.Status)
	}
	if len(swp.swept) != 1 {
		t.Fatalf("late deposit must sweep, got %v", swp.swept)
	}
}

func vaultRef(t *testing.T, enc, masterKey []byte) string {
	t.Helper()
	seed, err := wallet.DecryptSecret(enc, masterKey)
	if err != nil {
		t.Fatal(err)
	}
	return base58ish(seed) // test-only stand-in pubkey
}

func rowRef(t *testing.T, db *store.Store, paymentID string) string {
	t.Helper()
	row, err := db.GetDepositKey(context.Background(), paymentID)
	if err != nil || row == nil {
		t.Fatalf("deposit key row: %v %v", row, err)
	}
	return row.PublicKey
}

func logNop(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(os.Stderr, nil))
}
