package watcher

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/mufalito/oalgosolana/internal/chain"
	"github.com/mufalito/oalgosolana/internal/solanapay"

	"github.com/google/uuid"
	"github.com/mufalito/oalgosolana/internal/store"
)

// fakeDetector drives the state machine deterministically.
type fakeDetector struct {
	sigs     []string                 // returned by SignaturesForAddress
	txs      map[string]*chain.TxInfo // signature -> tx
	commitOK map[string]bool          // signature -> available at "finalized"
	fetchErr map[string]error
}

func (f *fakeDetector) SignaturesForAddress(_ context.Context, _ string) ([]string, error) {
	return f.sigs, nil
}

func (f *fakeDetector) Transaction(_ context.Context, sig, _ string, commitment string) (*chain.TxInfo, error) {
	if err := f.fetchErr[sig]; err != nil {
		return nil, err
	}
	if commitment == "finalized" && !f.commitOK[sig] {
		return nil, &chain.NotFoundError{Method: "getTransaction"}
	}
	tx := f.txs[sig]
	if tx == nil {
		return nil, &chain.NotFoundError{Method: "getTransaction"}
	}
	cp := *tx
	return &cp, nil
}

// testURL gives each package its own database so parallel test packages never
// truncate each other's rows.
func testURL(t *testing.T) string {
	t.Helper()
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		base = "postgres://solanapay:solanapay@localhost:5432/postgres?sslmode=disable"
	}
	name := "solanapay_test_watcher"
	create := "CREATE DATABASE " + name
	exec := func(q string) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		c, err := pgx.Connect(ctx, base)
		if err != nil {
			t.Skipf("database not reachable: %v", err)
		}
		defer c.Close(context.Background())
		_, _ = c.Exec(ctx, q)
	}
	exec(create)
	exec("DROP DATABASE IF EXISTS " + name + "_old")
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
	// clean slate
	if _, err := st.Pool.Exec(ctx, `TRUNCATE webhook_deliveries, payment_txs, deposit_keys, payments`); err != nil {
		t.Fatal(err)
	}
	return st
}

func mkTx(merchant, ref string, lamports uint64) *chain.TxInfo {
	return &chain.TxInfo{
		Signature:              "sig_" + ref[:8],
		RecipientDeltaLamports: lamports,
		TokenDeltas:            map[string]uint64{},
		AccountKeys:            []string{"SomeSender11111111111111111111111111111111111", merchant, ref},
	}
}

func insertPayment(t *testing.T, db *store.Store, status string, txSig string) *store.Payment {
	t.Helper()
	ref, _ := newRef()
	p := &store.Payment{
		ID: uuid.Must(uuid.NewV7()).String(), OrderID: "ord_" + ref[:8],
		AmountBase: 1500000000, Currency: "SOL", Status: status,
		Reference: ref, MerchantWallet: testMerchant, WebhookURL: "https://example.com/hook",
		ExpiresAt: time.Now().Add(30 * time.Minute), CreatedAt: time.Now(),
		TxSignature: txSig,
	}
	if err := db.CreatePayment(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	return p
}

const testMerchant = "9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM"

func newRef() (string, error) {
	pk, err := solanapay.NewReference()
	return pk.String(), err
}

func newWatcher(db *store.Store, det chain.Detector, policy string) *Watcher {
	return &Watcher{
		DB: db, Detector: det, Log: slog.New(slog.NewTextHandler(os.Stderr, nil)),
		Cfg: WatcherConfig{ConfirmationPolicy: policy, PollInterval: 10 * time.Millisecond, Workers: 2},
	}
}

func TestPendingToConfirmedToFinalized(t *testing.T) {
	db := testDB(t)
	p := insertPayment(t, db, "pending", "")
	tx := mkTx(testMerchant, p.Reference, 1500000000)
	det := &fakeDetector{
		sigs:     []string{tx.Signature},
		txs:      map[string]*chain.TxInfo{tx.Signature: tx},
		commitOK: map[string]bool{tx.Signature: false}, // finalized later
	}
	w := newWatcher(db, det, "confirmed")
	ctx := context.Background()

	// tick 1: pending -> processing
	if err := w.pollOnce(ctx); err != nil {
		t.Fatal(err)
	}
	row, _ := db.GetPayment(ctx, uuidMustParse(p.ID))
	if row.Status != "processing" || row.TxSignature != tx.Signature {
		t.Fatalf("after tick1: status=%s sig=%s", row.Status, row.TxSignature)
	}

	// tick 2: processing -> confirmed + webhook enqueued
	if err := w.pollOnce(ctx); err != nil {
		t.Fatal(err)
	}
	row, _ = db.GetPayment(ctx, uuidMustParse(p.ID))
	if row.Status != "confirmed" || row.ConfirmedAt == nil {
		t.Fatalf("after tick2: status=%s confirmed_at=%v", row.Status, row.ConfirmedAt)
	}
	var n int
	db.Pool.QueryRow(ctx, `SELECT count(*) FROM webhook_deliveries WHERE payment_id=$1 AND event_type='payment.confirmed'`, p.ID).Scan(&n)
	if n != 1 {
		t.Fatalf("confirmed webhook count = %d", n)
	}

	// tick 3: confirmed (tx not yet finalized) -> stays confirmed
	if err := w.pollOnce(ctx); err != nil {
		t.Fatal(err)
	}
	row, _ = db.GetPayment(ctx, uuidMustParse(p.ID))
	if row.Status != "confirmed" {
		t.Fatalf("after tick3: status=%s", row.Status)
	}

	// tx reaches finality: tick 4 -> finalized + webhook
	det.commitOK[tx.Signature] = true
	if err := w.pollOnce(ctx); err != nil {
		t.Fatal(err)
	}
	row, _ = db.GetPayment(ctx, uuidMustParse(p.ID))
	if row.Status != "finalized" || row.FinalizedAt == nil {
		t.Fatalf("after tick4: status=%s", row.Status)
	}
	db.Pool.QueryRow(ctx, `SELECT count(*) FROM webhook_deliveries WHERE payment_id=$1 AND event_type='payment.finalized'`, p.ID).Scan(&n)
	if n != 1 {
		t.Fatalf("finalized webhook count = %d", n)
	}
}

func TestWrongAmountFails(t *testing.T) {
	db := testDB(t)
	p := insertPayment(t, db, "pending", "")
	tx := mkTx(testMerchant, p.Reference, 1400000000) // underpaid
	det := &fakeDetector{sigs: []string{tx.Signature}, txs: map[string]*chain.TxInfo{tx.Signature: tx}}
	w := newWatcher(db, det, "confirmed")
	ctx := context.Background()
	for range 2 {
		if err := w.pollOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	row, _ := db.GetPayment(ctx, uuidMustParse(p.ID))
	if row.Status != "failed" || row.FailureReason != "WRONG_AMOUNT" {
		t.Fatalf("got status=%s reason=%s", row.Status, row.FailureReason)
	}
}

func TestDuplicateSignatureSingleTransition(t *testing.T) {
	db := testDB(t)
	p := insertPayment(t, db, "pending", "")
	tx := mkTx(testMerchant, p.Reference, 1500000000)
	det := &fakeDetector{
		sigs:     []string{tx.Signature},
		txs:      map[string]*chain.TxInfo{tx.Signature: tx},
		commitOK: map[string]bool{tx.Signature: true},
	}
	w := newWatcher(db, det, "confirmed")
	ctx := context.Background()
	for range 5 { // many ticks: idempotent
		if err := w.pollOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	db.Pool.QueryRow(ctx, `SELECT count(*) FROM webhook_deliveries WHERE payment_id=$1`, p.ID).Scan(&n)
	if n != 2 { // exactly confirmed + finalized, nothing more
		t.Fatalf("webhook deliveries = %d, want 2", n)
	}
	var txRows int
	db.Pool.QueryRow(ctx, `SELECT count(*) FROM payment_txs WHERE payment_id=$1`, p.ID).Scan(&txRows)
	if txRows != 1 {
		t.Fatalf("payment_txs rows = %d", txRows)
	}
}

func TestNoWebhookWhenUnconfigured(t *testing.T) {
	db := testDB(t)
	p := insertPayment(t, db, "pending", "")
	// remove webhook
	db.Pool.Exec(context.Background(), `UPDATE payments SET webhook_url='' WHERE id=$1`, p.ID)
	tx := mkTx(testMerchant, p.Reference, 1500000000)
	det := &fakeDetector{sigs: []string{tx.Signature}, txs: map[string]*chain.TxInfo{tx.Signature: tx}}
	w := newWatcher(db, det, "confirmed")
	for range 2 {
		if err := w.pollOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM webhook_deliveries WHERE payment_id=$1`, p.ID).Scan(&n)
	if n != 0 {
		t.Fatalf("deliveries = %d, want 0", n)
	}
}

func uuidMustParse(s string) uuid.UUID {
	return uuid.Must(uuid.Parse(s))
}
