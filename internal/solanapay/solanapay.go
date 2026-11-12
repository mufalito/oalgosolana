// Package solanapay contains the PURE core of Solana Pay support: decimal
// amount parsing/formatting, transfer-request URL building, reference key
// generation, and QR encoding. No I/O lives here; everything is unit-testable.
package solanapay

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"strings"

	"github.com/gagliardetto/solana-go"
	"github.com/skip2/go-qrcode"
)

// Currency base-unit decimals.
const (
	SOLDecimals  = 9
	USDCDecimals = 6
)

// Currency enumerates supported currencies.
type Currency string

const (
	SOL  Currency = "SOL"
	USDC Currency = "USDC"
)

// Decimals returns the base-unit precision of the currency.
func (c Currency) Decimals() int {
	if c == USDC {
		return USDCDecimals
	}
	return SOLDecimals
}

var (
	ErrInvalidAmount = errors.New("invalid amount")
	maxUint64        = new(big.Int).SetUint64(^uint64(0))
)

// ParseAmount parses a decimal string like "1.50" into base units (uint64)
// using exact fixed-point arithmetic. It rejects signs, exponents, more
// fractional digits than the currency allows, >18 integer digits, empty
// strings, and leading/trailing junk. Never uses float64.
func ParseAmount(s string, c Currency) (uint64, error) {
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
	dec := c.Decimals()
	if len(fracPart) > dec {
		return 0, fmt.Errorf("%w: more than %d fractional digits", ErrInvalidAmount, dec)
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
	// scale = 10^(dec - len(frac))
	scaleExp := dec
	if hasFrac {
		scaleExp = dec - len(fracPart)
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

// FormatAmount renders base units as a plain decimal string with the
// currency's precision, e.g. 1500000000 SOL -> "1.500000000".
func FormatAmount(base uint64, c Currency) string {
	dec := c.Decimals()
	if dec == 0 {
		return new(big.Int).SetUint64(base).String()
	}
	div, mod := new(big.Int), new(big.Int)
	div.DivMod(new(big.Int).SetUint64(base), pow10(dec), mod)
	frac := mod.String()
	if len(frac) < dec {
		frac = strings.Repeat("0", dec-len(frac)) + frac
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

// NewReference returns a fresh, cryptographically random reference pubkey.
// Random ed25519 public keys are valid, off-curve-free (PDA-free) pubkeys.
func NewReference() (solana.PublicKey, error) {
	_, pub, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return solana.PublicKey{}, err
	}
	var pk solana.PublicKey
	copy(pk[:], pub)
	return pk, nil
}

// BuildURL constructs a native Solana Pay transfer-request URL:
// solana:<recipient>?amount=<decimal>&spl-token=<mint>&reference=<ref>&label=...&message=...
// spl-token is omitted for SOL.
func BuildURL(recipient solana.PublicKey, amountBase uint64, c Currency, mint string, reference solana.PublicKey, label, message string) string {
	q := url.Values{}
	q.Set("amount", FormatAmount(amountBase, c))
	if c == USDC && mint != "" {
		q.Set("spl-token", mint)
	}
	q.Set("reference", reference.String())
	if label != "" {
		q.Set("label", label)
	}
	if message != "" {
		q.Set("message", message)
	}
	return "solana:" + recipient.String() + "?" + q.Encode()
}

// QRCodePNG renders the URL as a PNG QR code.
func QRCodePNG(url string) ([]byte, error) {
	return qrcode.Encode(url, qrcode.Medium, 256)
}
