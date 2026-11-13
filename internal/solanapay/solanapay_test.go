package solanapay

import (
	"testing"
)

func TestParseAmount(t *testing.T) {
	cases := []struct {
		in   string
		want uint64
		ok   bool
	}{
		{"1.50", 1500000000, true},
		{"1.5", 1500000000, true},
		{"1", 1000000000, true},
		{"0.000000001", 1, true},
		{"1.000000001", 1000000001, true},
		{".5", 500000000, true},
		{"1.5000000001", 0, false},        // over-precision
		{"", 0, false},                    // empty
		{"   ", 0, false},                 // whitespace only
		{"-1", 0, false},                  // sign
		{"+1", 0, false},                  // sign
		{"1e9", 0, false},                 // exponent
		{"1.5.5", 0, false},               // multiple dots
		{"1.", 0, false},                  // missing frac
		{"abc", 0, false},                 // junk
		{"1 SOL", 0, false},               // junk
		{"0", 0, false},                   // zero
		{"9999999999999999999", 0, false}, // 19 digits
	}
	for _, tc := range cases {
		got, err := ParseAmount(tc.in)
		if tc.ok && err != nil {
			t.Errorf("ParseAmount(%q): unexpected error %v", tc.in, err)
			continue
		}
		if !tc.ok {
			if err == nil {
				t.Errorf("ParseAmount(%q): expected error, got %d", tc.in, got)
			}
			continue
		}
		if got != tc.want {
			t.Errorf("ParseAmount(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestFormatAmount(t *testing.T) {
	if got := FormatAmount(1500000000); got != "1.500000000" {
		t.Errorf("format = %q", got)
	}
	if got := FormatAmount(1); got != "0.000000001" {
		t.Errorf("minimal = %q", got)
	}
}

func TestParseFormatRoundtrip(t *testing.T) {
	for _, s := range []string{"1", "0.000000001", "123.456789", "999999999.999999999"} {
		base, err := ParseAmount(s)
		if err != nil {
			t.Fatalf("parse %q: %v", s, err)
		}
		got := FormatAmount(base)
		base2, err := ParseAmount(got)
		if err != nil || base2 != base {
			t.Errorf("roundtrip %q -> %q: %d vs %d (err %v)", s, got, base, base2, err)
		}
	}
}

func TestBuildURL(t *testing.T) {
	u := BuildURL("9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM", "Coffee Shop", "order 42", 1500000000)
	want := "solana:9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM?amount=1.500000000&label=Coffee+Shop&message=order+42"
	if u != want {
		t.Fatalf("got %q", u)
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
