// Package solanapay contains the PURE money/URL/QR helpers: exact fixed-point
// amount parsing and formatting (SOL, 9 decimals), Solana Pay transfer URLs
// without a reference, and QR encoding. No I/O lives here.
package solanapay

import (
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"strings"

	"github.com/skip2/go-qrcode"
)

// SOLDecimals is lamports precision (10^9 lamports = 1 SOL).
const SOLDecimals = 9

// MaxDecimals bounds accepted mint precision.
const MaxDecimals = 18

var (
	ErrInvalidAmount = errors.New("invalid amount")
	maxUint64        = new(big.Int).SetUint64(^uint64(0))
)

// ParseAmount parses a decimal string like "1.50" into base units at the
// given decimals (9 for SOL, the mint's decimals for tokens) using exact
// fixed-point arithmetic. It rejects signs, exponents, over-precision,
// >18 integer digits, empty strings, and junk. Never uses float64.
func ParseAmount(s string, decimals int) (uint64, error) {
	if decimals < 0 || decimals > MaxDecimals {
		return 0, fmt.Errorf("%w: invalid decimals %d", ErrInvalidAmount, decimals)
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("%w: empty string", ErrInvalidAmount)
	}
	intPart, fracPart, hasFrac := strings.Cut(s, ".")
	if strings.Count(s, ".") > 1 {
		return 0, fmt.Errorf("%w: multiple decimal points", ErrInvalidAmount)
	}
	if intPart == "" {
		intPart = "0"
	}
	if hasFrac && fracPart == "" {
		return 0, fmt.Errorf("%w: missing fractional digits", ErrInvalidAmount)
	}
	if !allDigits(intPart) || (hasFrac && !allDigits(fracPart)) {
		return 0, fmt.Errorf("%w: non-digit characters", ErrInvalidAmount)
	}
	if len(intPart) > 18 {
		return 0, fmt.Errorf("%w: too many integer digits", ErrInvalidAmount)
	}
	if len(fracPart) > decimals {
		return 0, fmt.Errorf("%w: more than %d fractional digits", ErrInvalidAmount, decimals)
	}
	value := new(big.Int)
	if intPart != "0" || fracPart != "" {
		digits := intPart + fracPart
		v, ok := new(big.Int).SetString(digits, 10)
		if !ok {
			return 0, fmt.Errorf("%w: unparseable digits", ErrInvalidAmount)
		}
		value = v
	}
	scaleExp := decimals
	if hasFrac {
		scaleExp = decimals - len(fracPart)
	}
	value.Mul(value, pow10(scaleExp))
	if value.Sign() == 0 {
		return 0, fmt.Errorf("%w: amount must be positive", ErrInvalidAmount)
	}
	if value.Cmp(maxUint64) > 0 {
		return 0, fmt.Errorf("%w: exceeds uint64", ErrInvalidAmount)
	}
	return value.Uint64(), nil
}

// FormatAmount renders base units at the given decimals as a plain decimal
// string, e.g. FormatAmount(1500000000, 9) -> "1.500000000".
func FormatAmount(base uint64, decimals int) string {
	div, mod := new(big.Int), new(big.Int)
	div.DivMod(new(big.Int).SetUint64(base), pow10(decimals), mod)
	if decimals == 0 {
		return div.String()
	}
	frac := mod.String()
	if len(frac) < decimals {
		frac = strings.Repeat("0", decimals-len(frac)) + frac
	}
	return div.String() + "." + frac
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

var pow10Cache = func() [20]*big.Int {
	var a [20]*big.Int
	a[0] = big.NewInt(1)
	for i := 1; i < 20; i++ {
		a[i] = new(big.Int).Mul(a[i-1], big.NewInt(10))
	}
	return a
}()

func pow10(n int) *big.Int { return pow10Cache[n] }

// BuildURL constructs a Solana Pay transfer-request URL for a deposit vault:
// solana:<address>?amount=<decimal>[&spl-token=<mint>]&label=...&message=...
func BuildURL(address, label, message string, amountBase uint64, decimals int, mint string) string {
	q := url.Values{}
	q.Set("amount", FormatAmount(amountBase, decimals))
	if mint != "" {
		q.Set("spl-token", mint)
	}
	if label != "" {
		q.Set("label", label)
	}
	if message != "" {
		q.Set("message", message)
	}
	return "solana:" + address + "?" + q.Encode()
}

// QRCodePNG renders the given text (address or solana: URL) as a PNG QR.
func QRCodePNG(url string) ([]byte, error) {
	return qrcode.Encode(url, qrcode.Medium, 256)
}
