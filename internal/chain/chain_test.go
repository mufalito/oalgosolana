package chain

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mufalito/oalgosolana/internal/store"
)

const merchant = "9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM"
const refKey = "E1GaxPQ4xr6PWT2BPY5tLGfLnwsNaNdABqknaGfcC5AF"
const otherKey = "4Nd1mBQtrMJVYVfKf2PJy9NZUZdTAsp7D4xWLs4gDB4T"

// fixtureJSON builds a getTransaction result for a simple SOL transfer of
// amountBase lamports from sender to merchant, including the reference key.
func fixtureSOL(amountBase uint64, withRef bool) string {
	accounts := []string{otherKey, merchant}
	if withRef {
		accounts = append(accounts, refKey)
	}
	pre := make([]uint64, len(accounts))
	post := make([]uint64, len(accounts))
	for i := range accounts {
		pre[i], post[i] = 1000000, 1000000
	}
	post[1] += amountBase
	acc, _ := json.Marshal(accounts)
	pr, _ := json.Marshal(pre)
	po, _ := json.Marshal(post)
	return `{"result":{"slot":1,"transaction":{"message":{"accountKeys":` + string(acc) +
		`}},"meta":{"err":null,"preBalances":` + string(pr) + `,"postBalances":` + string(po) +
		`,"preTokenBalances":[],"postTokenBalances":[]}},"id":1}`
}

func fixtureUSDC(mint string, amount uint64) string {
	accounts := []string{otherKey, merchant, refKey}
	pre := []uint64{1000000, 1000000, 1000000}
	post := []uint64{1000000, 1000000, 1000000}
	acc, _ := json.Marshal(accounts)
	pr, _ := json.Marshal(pre)
	po, _ := json.Marshal(post)
	return `{"result":{"slot":1,"transaction":{"message":{"accountKeys":` + string(acc) +
		`}},"meta":{"err":null,"preBalances":` + string(pr) + `,"postBalances":` + string(po) +
		`,"preTokenBalances":[{"accountIndex":3,"mint":"` + mint + `","owner":"` + merchant + `","amount":"1000"},{"accountIndex":4,"mint":"` + "DezXAZ8z7PnrnRJjz3wXBoRgixCaBRhpWh4bFcSbZ9B" + `","owner":"` + otherKey + `","amount":"5"}],` +
		`"postTokenBalances":[{"accountIndex":3,"mint":"` + mint + `","owner":"` + merchant + `","amount":"` + u64s(1000+amount) + `"},{"accountIndex":4,"mint":"DezXAZ8z7PnrnRJjz3wXBoRgixCaBRhpWh4bFcSbZ9B","owner":"` + otherKey + `","amount":"5"}]}},"id":1}`
}

func u64s(n uint64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func newTestDetector(t *testing.T, body string) *RPCDetector {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return NewRPCDetector(srv.URL, 100)
}

func TestValidateSOL(t *testing.T) {
	p := &store.Payment{AmountBase: 1500000000, Currency: "SOL", Reference: refKey, MerchantWallet: merchant}
	d := newTestDetector(t, fixtureSOL(1500000000, true))
	tx, err := d.Transaction(context.Background(), "sig1", merchant, "confirmed")
	if err != nil {
		t.Fatal(err)
	}
	if r := Validate(tx, p, ""); r.Failed() {
		t.Fatalf("expected valid, got %s", r)
	}
}

func TestValidateSOLWrongAmount(t *testing.T) {
	p := &store.Payment{AmountBase: 1500000000, Currency: "SOL", Reference: refKey, MerchantWallet: merchant}
	d := newTestDetector(t, fixtureSOL(1400000000, true))
	tx, _ := d.Transaction(context.Background(), "sig1", merchant, "confirmed")
	if r := Validate(tx, p, ""); r != WrongAmount {
		t.Fatalf("expected WRONG_AMOUNT, got %s", r)
	}
}

func TestValidateMissingReference(t *testing.T) {
	p := &store.Payment{AmountBase: 1500000000, Currency: "SOL", Reference: refKey, MerchantWallet: merchant}
	d := newTestDetector(t, fixtureSOL(1500000000, false))
	tx, _ := d.Transaction(context.Background(), "sig1", merchant, "confirmed")
	if r := Validate(tx, p, ""); r != MissingReference {
		t.Fatalf("expected MISSING_REFERENCE, got %s", r)
	}
}

func TestValidateWrongRecipient(t *testing.T) {
	p := &store.Payment{AmountBase: 1500000000, Currency: "SOL", Reference: refKey, MerchantWallet: "SomeOtherWallet1111111111111111111111111111111"}
	d := newTestDetector(t, fixtureSOL(1500000000, true))
	tx, _ := d.Transaction(context.Background(), "sig1", merchant, "confirmed")
	if r := Validate(tx, p, ""); r != WrongRecipient {
		t.Fatalf("expected WRONG_RECIPIENT, got %s", r)
	}
}

func TestValidateUSDC(t *testing.T) {
	mint := "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"
	p := &store.Payment{AmountBase: 2500000, Currency: "USDC", Reference: refKey, MerchantWallet: merchant}
	d := newTestDetector(t, fixtureUSDC(mint, 2500000))
	tx, err := d.Transaction(context.Background(), "sig1", merchant, "confirmed")
	if err != nil {
		t.Fatal(err)
	}
	if r := Validate(tx, p, mint); r.Failed() {
		t.Fatalf("expected valid, got %s", r)
	}
	// wrong mint paid
	d2 := newTestDetector(t, fixtureUSDC("DezXAZ8z7PnrnRJjz3wXBoRgixCaBRhpWh4bFcSbZ9B", 2500000))
	tx2, _ := d2.Transaction(context.Background(), "sig1", merchant, "confirmed")
	if r := Validate(tx2, p, mint); r != WrongMint {
		t.Fatalf("expected WRONG_MINT, got %s", r)
	}
	// right mint wrong amount
	d3 := newTestDetector(t, fixtureUSDC(mint, 2400000))
	tx3, _ := d3.Transaction(context.Background(), "sig1", merchant, "confirmed")
	if r := Validate(tx3, p, mint); r != WrongAmount {
		t.Fatalf("expected WRONG_AMOUNT, got %s", r)
	}
}

func TestSignaturesForAddress(t *testing.T) {
	body := `{"result":[{"signature":"sigA","err":null},{"signature":"sigB","err":{"message":"failed"}},{"signature":"sigC","err":null}],"id":1}`
	d := newTestDetector(t, body)
	sigs, err := d.SignaturesForAddress(context.Background(), refKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(sigs) != 2 || sigs[0] != "sigA" || sigs[1] != "sigC" {
		t.Fatalf("got %v", sigs)
	}
}

func TestTxNotFound(t *testing.T) {
	d := newTestDetector(t, `{"result":null,"id":1}`)
	_, err := d.Transaction(context.Background(), "sigX", merchant, "confirmed")
	if !IsNotFound(err) {
		t.Fatalf("expected NotFoundError, got %v", err)
	}
}
