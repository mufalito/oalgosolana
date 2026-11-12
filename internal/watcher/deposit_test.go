package watcher

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mufalito/oalgosolana/internal/chain"
	"github.com/mufalito/oalgosolana/internal/store"
)

// fakeVaultSweeper implements DepositSweeper and records sweeps per invoice.
type fakeVaultSweeper struct {
	mu        sync.Mutex
	swept     map[string]int // invoice id -> sweep attempts
	sigFor    map[string]string
	state     map[string]SweepState // sweep sig -> state
	failSweep bool
}

func newFakeSweeper() *fakeVaultSweeper {
	return &fakeVaultSweeper{swept: map[string]int{}, sigFor: map[string]string{}, state: map[string]SweepState{}}
}

func (f *fakeVaultSweeper) SweepVault(_ context.Context, invoiceID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failSweep {
		return "", errBoom
	}
	f.swept[invoiceID]++
	sig := "sweep_" + invoiceID[:8] + "_" + itoa(f.swept[invoiceID])
	f.sigFor[invoiceID] = sig
	f.state[sig] = SweepPending // in flight
	return sig, nil
}

func (f *fakeVaultSweeper) CheckSweep(_ context.Context, sig string) (SweepState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state[sig], nil
}

var errBoom = boom{}

type boom struct{}

func (boom) Error() string { return "sweep send failed" }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// depositDetector serves balances and one incoming signature.
type depositDetector struct {
	fakeDetector
	vaultAddr string
	balance   uint64
}

func (d *depositDetector) GetBalance(_ context.Context, addr string) (uint64, error) {
	if addr == d.vaultAddr {
		return d.balance, nil
	}
	return 0, nil
}

func (d *depositDetector) SignaturesForAddress(_ context.Context, ref string) ([]string, error) {
	if ref == d.vaultAddr && d.balance > 0 {
		return []string{"incoming_sig_1"}, nil
	}
	return nil, nil
}

func newDepositWatcher(db *store.Store, det chain.Detector, swp *fakeVaultSweeper) *Watcher {
	return &Watcher{
		DB: db, Detector: det, Log: slog.New(slog.NewTextHandler(os.Stderr, nil)),
		Sweeper: swp,
		Cfg: WatcherConfig{
			ConfirmationPolicy: "confirmed", PollInterval: time.Millisecond,
			Workers: 2, DepositMode: true,
		},
	}
}

func insertDepositPayment(t *testing.T, db *store.Store, status, sweepTx string, amount uint64) *store.Payment {
	t.Helper()
	p := &store.Payment{
		ID: uuid.Must(uuid.NewV7()).String(), OrderID: "dep_" + uuid.NewString()[:8],
		AmountBase: amount, Currency: "SOL", Status: status,
		Reference: "VaultAddr" + uuid.NewString()[:31], MerchantWallet: testMerchant,
		WebhookURL: "https://example.com/hook",
		ExpiresAt:  time.Now().Add(30 * time.Minute), CreatedAt: time.Now(),
		SweepTx: sweepTx,
	}
	if err := db.CreatePayment(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDepositFlowBalanceToFinalized(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	p := insertDepositPayment(t, db, "pending", "", 20_000_000)
	swp := newFakeSweeper()
	det := &depositDetector{vaultAddr: p.Reference, balance: 20_000_000}
	w := newDepositWatcher(db, det, swp)

	// tick 1: balance >= amount -> sweep sent + confirmed
	if err := w.pollOnce(ctx); err != nil {
		t.Fatal(err)
	}
	row, _ := db.GetPayment(ctx, uuid.MustParse(p.ID))
	if row.Status != "confirmed" || row.ConfirmedAt == nil || row.SweepTx == "" {
		t.Fatalf("after tick1: %+v", row)
	}
	if row.TxSignature != "incoming_sig_1" {
		t.Fatalf("receipt signature = %q", row.TxSignature)
	}

	// tick 2: sweep not landed yet -> stays confirmed
	if err := w.pollOnce(ctx); err != nil {
		t.Fatal(err)
	}
	row, _ = db.GetPayment(ctx, uuid.MustParse(p.ID))
	if row.Status != "confirmed" {
		t.Fatalf("after tick2: %s", row.Status)
	}

	// sweep lands -> finalized + webhooks
	swp.mu.Lock()
	swp.state[row.SweepTx] = SweepLanded
	swp.mu.Unlock()
	if err := w.pollOnce(ctx); err != nil {
		t.Fatal(err)
	}
	row, _ = db.GetPayment(ctx, uuid.MustParse(p.ID))
	if row.Status != "finalized" || row.FinalizedAt == nil {
		t.Fatalf("after tick3: %s", row.Status)
	}
	var n int
	db.Pool.QueryRow(ctx, `SELECT count(*) FROM webhook_deliveries WHERE payment_id=$1`, p.ID).Scan(&n)
	if n != 2 { // confirmed + finalized
		t.Fatalf("deliveries = %d", n)
	}
}

func TestDepositPartialPaymentKeepsWatching(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	p := insertDepositPayment(t, db, "pending", "", 20_000_000)
	swp := newFakeSweeper()
	det := &depositDetector{vaultAddr: p.Reference, balance: 5_000_000} // 25%
	w := newDepositWatcher(db, det, swp)

	for range 3 {
		if err := w.pollOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	row, _ := db.GetPayment(ctx, uuid.MustParse(p.ID))
	if row.Status != "processing" {
		t.Fatalf("partial payment must stay processing, got %s", row.Status)
	}
	swp.mu.Lock()
	swept := len(swp.swept)
	swp.mu.Unlock()
	if swept != 0 {
		t.Fatal("must not sweep a partial payment")
	}
}

func TestDepositFailedSweepIsResent(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	p := insertDepositPayment(t, db, "pending", "", 20_000_000)
	swp := newFakeSweeper()
	det := &depositDetector{vaultAddr: p.Reference, balance: 20_000_000}
	w := newDepositWatcher(db, det, swp)

	// tick 1: sweep sent, confirmed
	if err := w.pollOnce(ctx); err != nil {
		t.Fatal(err)
	}
	row, _ := db.GetPayment(ctx, uuid.MustParse(p.ID))
	firstSig := row.SweepTx
	if firstSig == "" || row.Status != "confirmed" {
		t.Fatalf("tick1: %+v", row)
	}

	// pending sweep must NOT be re-sent
	if err := w.pollOnce(ctx); err != nil {
		t.Fatal(err)
	}
	row, _ = db.GetPayment(ctx, uuid.MustParse(p.ID))
	if row.SweepTx != firstSig {
		t.Fatal("in-flight sweep must not be re-sent")
	}

	// the sweep FAILS on-chain: the vault still holds funds, so a new sweep
	// must be sent on the next tick.
	swp.mu.Lock()
	swp.state[firstSig] = SweepFailed
	swp.mu.Unlock()
	if err := w.pollOnce(ctx); err != nil {
		t.Fatal(err)
	}
	row, _ = db.GetPayment(ctx, uuid.MustParse(p.ID))
	if row.SweepTx == firstSig {
		t.Fatal("failed sweep must be re-sent with a new signature")
	}
	swp.mu.Lock()
	attempts := swp.swept[p.ID]
	swp.mu.Unlock()
	if attempts != 2 {
		t.Fatalf("sweep attempts = %d, want 2", attempts)
	}
}

func TestSweepLateDepositExpired(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	p := &store.Payment{
		ID: uuid.Must(uuid.NewV7()).String(), OrderID: "dep_" + uuid.NewString()[:8],
		AmountBase: 20_000_000, Currency: "SOL", Status: "expired",
		Reference: "VaultAddr" + uuid.NewString()[:31], MerchantWallet: testMerchant,
		WebhookURL: "https://example.com/hook",
		ExpiresAt:  time.Now().Add(-time.Minute), CreatedAt: time.Now(),
	}
	db.CreatePayment(ctx, p)
	swp := newFakeSweeper()
	det := &depositDetector{vaultAddr: p.Reference, balance: 20_000_000}
	w := newDepositWatcher(db, det, swp)

	if err := w.SweepLateDeposit(ctx, p); err != nil {
		t.Fatal(err)
	}
	row, _ := db.GetPayment(ctx, uuid.MustParse(p.ID))
	if row.Status != "confirmed" || row.SweepTx == "" {
		t.Fatalf("late deposit: %+v", row)
	}
	swp.mu.Lock()
	attempts := swp.swept[p.ID]
	swp.mu.Unlock()
	if attempts != 1 {
		t.Fatalf("late deposit must sweep, attempts=%d", attempts)
	}
}
