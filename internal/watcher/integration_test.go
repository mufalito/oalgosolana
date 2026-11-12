package watcher

import (
	"context"
	"crypto/rand"
	"os"
	"testing"
	"time"

	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/programs/system"
	"github.com/gagliardetto/solana-go/rpc"
	"github.com/google/uuid"

	"github.com/mufalito/oalgosolana/internal/chain"
	"github.com/mufalito/oalgosolana/internal/store"
)

// TestIntegrationValidator drives a real transfer against a local
// solana-test-validator (SOLANA_TEST_VALIDATOR_URL). Skipped when unset.
func TestIntegrationValidator(t *testing.T) {
	valURL := integrationURL(t)
	if valURL == "" {
		t.Skip("SOLANA_TEST_VALIDATOR_URL not set")
	}
	db := testDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	det := chain.NewRPCDetector(valURL, 1000)

	// throwaway payer, airdrop 1 SOL
	payer, err := solana.NewRandomPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	rc := rpc.New(valURL)
	if _, err := rc.RequestAirdrop(ctx, payer.PublicKey(), 1_000_000_000, rpc.CommitmentConfirmed); err != nil {
		t.Skipf("airdrop failed: %v", err)
	}
	// merchant is a fresh wallet on the validator
	merchant, _ := solana.NewRandomPrivateKey()

	ref, err := newRef()
	if err != nil {
		t.Fatal(err)
	}
	refPK, _ := solana.PublicKeyFromBase58(ref)
	amount := uint64(1_500_000_000)

	p := &store.Payment{
		ID: uuid.Must(uuid.NewV7()).String(), OrderID: "int_" + ref[:8],
		AmountBase: amount, Currency: "SOL", Status: "pending",
		Reference: ref, MerchantWallet: merchant.PublicKey().String(),
		ExpiresAt: time.Now().Add(30 * time.Minute), CreatedAt: time.Now(),
	}
	if err := db.CreatePayment(ctx, p); err != nil {
		t.Fatal(err)
	}

	// build + send transfer with the reference as an extra account key
	recent, err := rc.GetLatestBlockhash(ctx, rpc.CommitmentConfirmed)
	if err != nil {
		t.Skipf("blockhash: %v", err)
	}
	// SOL transfer with the reference attached as an extra account key of a
	// 0-lamport transfer (the standard reference-in-account-keys pattern).
	tx, err := solana.NewTransaction(
		[]solana.Instruction{
			system.NewTransferInstruction(amount, payer.PublicKey(), merchant.PublicKey()).Build(),
			system.NewTransferInstruction(0, payer.PublicKey(), refPK).Build(),
		},
		recent.Value.Blockhash,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Sign(func(key solana.PublicKey) *solana.PrivateKey {
		if key.Equals(payer.PublicKey()) {
			return &payer
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := rc.SendTransactionWithOpts(ctx, tx, rpc.TransactionOpts{SkipPreflight: true}); err != nil {
		t.Skipf("send failed: %v", err)
	}

	w := newWatcher(db, det, "confirmed")
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if err := w.pollOnce(ctx); err != nil {
			t.Logf("poll: %v", err)
		}
		row, err := db.GetPayment(ctx, uuidMustParse(p.ID))
		if err != nil {
			t.Fatal(err)
		}
		if row.Status == "failed" {
			t.Fatalf("validation failed: %s", row.FailureReason)
		}
		if row.Status == "finalized" {
			return // confirmed -> finalized end-to-end
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatal("payment did not finalize in time")
}

func integrationURL(t *testing.T) string {
	t.Helper()
	u := os.Getenv("SOLANA_TEST_VALIDATOR_URL")
	if u == "" {
		// default local validator port, used only when reachable
		d := chain.NewRPCDetector("http://127.0.0.1:8899", 100)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if _, err := d.SignaturesForAddress(ctx, "11111111111111111111111111111111"); err == nil {
			return "http://127.0.0.1:8899"
		}
	}
	return u
}

var _ = rand.Reader
