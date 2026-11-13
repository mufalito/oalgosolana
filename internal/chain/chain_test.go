package chain

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestDetector(t *testing.T, body string) *RPCDetector {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return NewRPCDetector(srv.URL, 100)
}

func TestSignaturesForAddressSkipsFailed(t *testing.T) {
	d := newTestDetector(t, `{"result":[{"signature":"sigA","err":null},{"signature":"sigB","err":{"message":"failed"}},{"signature":"sigC","err":null}],"id":1}`)
	sigs, err := d.SignaturesForAddress(context.Background(), "SomeAddress")
	if err != nil {
		t.Fatal(err)
	}
	if len(sigs) != 2 || sigs[0] != "sigA" || sigs[1] != "sigC" {
		t.Fatalf("got %v", sigs)
	}
}

func TestGetBalance(t *testing.T) {
	d := newTestDetector(t, `{"result":{"context":{"slot":1},"value":123456},"id":1}`)
	bal, err := d.GetBalance(context.Background(), "SomeAddress")
	if err != nil || bal != 123456 {
		t.Fatalf("bal=%d err=%v", bal, err)
	}
}

func TestGetSignatureStatusesParsesErr(t *testing.T) {
	d := newTestDetector(t, `{"result":{"context":{"slot":1},"value":[{"slot":9,"confirmationStatus":"finalized","err":null},{"slot":8,"confirmationStatus":"processed","err":{"InstructionError":[0,{"Custom":1}]}}]},"id":1}`)
	st, err := d.GetSignatureStatuses(context.Background(), []string{"sigA", "sigB"})
	if err != nil || len(st) != 2 {
		t.Fatalf("st=%v err=%v", st, err)
	}
	if st[0].ConfirmationStatus != "finalized" || st[0].Err != nil {
		t.Fatalf("st0=%+v", st[0])
	}
	if st[1].Err == nil {
		t.Fatal("st1 err must be non-nil")
	}
}

func TestRentExemptMinimum(t *testing.T) {
	d := newTestDetector(t, `{"result":890880,"id":1}`)
	if m := d.RentExemptMinimum(context.Background()); m != 890880 {
		t.Fatalf("rent=%d", m)
	}
}
