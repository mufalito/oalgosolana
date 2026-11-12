package solanapay

import (
	"strings"
	"testing"

	"github.com/gagliardetto/solana-go"
)

func TestParseAmount(t *testing.T) {
	cases := []struct {
		in   string
		cur  Currency
		want uint64
		ok   bool
	}{
		{"1.50", SOL, 1500000000, true},
		{"1.5", SOL, 1500000000, true},
		{"1", SOL, 1000000000, true},
		{"0.000000001", SOL, 1, true},
		{"1.000000001", SOL, 1000000001, true},
		{"2.5", USDC, 2500000, true},
		{"0.000001", USDC, 1, true},
		// rejects
		{"1.5000000001", SOL, 0, false}, // over-precision
		{"0.0000001", USDC, 0, false},   // over-precision
		{"", SOL, 0, false},             // empty
		{"   ", SOL, 0, false},          // whitespace only
		{"-1", SOL, 0, false},           // sign
		{"+1", SOL, 0, false},           // sign
		{".5", SOL, 500000000, true},
		{"1.5.5", SOL, 0, false},                // multiple dots
		{"1.", SOL, 0, false},                   // missing frac
		{"abc", SOL, 0, false},                  // junk
		{"1 SOL", SOL, 0, false},                // junk
		{"0", SOL, 0, false},                    // zero
		{"0.000000000", SOL, 0, false},          // zero
		{"9999999999999999999", SOL, 0, false},  // 19 digits
		{"18446744073709551615", SOL, 0, false}, // fits uint64 but 20 digits
	}
	for _, tc := range cases {
		got, err := ParseAmount(tc.in, tc.cur)
		if tc.ok && err != nil {
			t.Errorf("ParseAmount(%q,%s): unexpected error %v", tc.in, tc.cur, err)
			continue
		}
		if !tc.ok {
			if err == nil {
				t.Errorf("ParseAmount(%q,%s): expected error, got %d", tc.in, tc.cur, got)
			}
			continue
		}
		if got != tc.want {
			t.Errorf("ParseAmount(%q,%s) = %d, want %d", tc.in, tc.cur, got, tc.want)
		}
	}
}

func TestFormatAmount(t *testing.T) {
	if got := FormatAmount(1500000000, SOL); got != "1.500000000" {
		t.Errorf("SOL format = %q", got)
	}
	if got := FormatAmount(2500000, USDC); got != "2.500000" {
		t.Errorf("USDC format = %q", got)
	}
	if got := FormatAmount(1, SOL); got != "0.000000001" {
		t.Errorf("SOL minimal = %q", got)
	}
}

func TestParseFormatRoundtrip(t *testing.T) {
	for _, s := range []string{"1", "0.000000001", "123.456789", "999999999.999999999"} {
		base, err := ParseAmount(s, SOL)
		if err != nil {
			t.Fatalf("parse %q: %v", s, err)
		}
		got := FormatAmount(base, SOL)
		// re-parse formatted value must yield the same base
		base2, err := ParseAmount(got, SOL)
		if err != nil || base2 != base {
			t.Errorf("roundtrip %q -> %q: %d vs %d (err %v)", s, got, base, base2, err)
		}
	}
}

func TestNewReference(t *testing.T) {
	a, err := NewReference()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewReference()
	if a.Equals(b) {
		t.Fatal("references must be unique")
	}
	if _, err := solana.PublicKeyFromBase58(a.String()); err != nil {
		t.Fatalf("reference not a valid base58 pubkey: %v", err)
	}
}

func TestBuildURL(t *testing.T) {
	recipient := mustPK("9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM")
	ref, _ := NewReference()
	// SOL
	u := BuildURL(recipient, 1500000000, SOL, "", ref, "Coffee Shop", "order 42")
	if !strings.HasPrefix(u, "solana:"+recipient.String()+"?") {
		t.Fatalf("bad prefix: %q", u)
	}
	if !strings.Contains(u, "amount=1.500000000") {
		t.Fatalf("missing amount: %q", u)
	}
	if !strings.Contains(u, "reference="+ref.String()) {
		t.Fatalf("missing reference: %q", u)
	}
	if strings.Contains(u, "spl-token") {
		t.Fatalf("SOL URL must not have spl-token: %q", u)
	}
	if !strings.Contains(u, "label=Coffee+Shop") {
		t.Fatalf("label not url-encoded: %q", u)
	}
	if !strings.Contains(u, "message=order+42") {
		t.Fatalf("message not url-encoded: %q", u)
	}
	// USDC
	mint := mustPK("EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v")
	u2 := BuildURL(recipient, 2500000, USDC, mint.String(), ref, "", "")
	if !strings.Contains(u2, "spl-token="+mint.String()) {
		t.Fatalf("USDC URL missing spl-token: %q", u2)
	}
	if !strings.Contains(u2, "amount=2.500000") {
		t.Fatalf("USDC amount wrong: %q", u2)
	}
}

func TestQRCodePNG(t *testing.T) {
	png, err := QRCodePNG("solana:9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM?amount=1.0")
	if err != nil {
		t.Fatal(err)
	}
	if len(png) == 0 || png[0] != 0x89 || png[1] != 'P' {
		t.Fatal("not a PNG")
	}
}

func mustPK(s string) solana.PublicKey {
	pk, err := solana.PublicKeyFromBase58(s)
	if err != nil {
		panic(err)
	}
	return pk
}
