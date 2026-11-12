package chain

import (
	"github.com/mufalito/oalgosolana/internal/solanapay"
	"github.com/mufalito/oalgosolana/internal/store"
)

// Reason is a validation failure code stored in payments.failure_reason.
type Reason string

const (
	Valid            Reason = ""
	WrongRecipient   Reason = "WRONG_RECIPIENT"
	WrongMint        Reason = "WRONG_MINT"
	WrongAmount      Reason = "WRONG_AMOUNT"
	MissingReference Reason = "MISSING_REFERENCE"
)

// Failed reports whether r is a validation failure (as opposed to Valid).
func (r Reason) Failed() bool { return r != Valid }

// Validate strictly checks a candidate tx against the expected payment:
//   - the merchant wallet appears in the tx (WRONG_RECIPIENT otherwise)
//   - the payment's reference pubkey is among the tx account keys
//   - SOL: recipient lamport delta equals amount_base
//   - USDC: recipient token delta on the configured mint equals amount_base
//
// Deltas must be computed for the payment's merchant wallet by the Detector.
// It never inspects SPL instruction data; amounts come from balance deltas.
func Validate(tx *TxInfo, p *store.Payment, usdcMint string) Reason {
	if !containsKey(tx.AccountKeys, p.MerchantWallet) {
		return WrongRecipient
	}
	if !containsKey(tx.AccountKeys, p.Reference) {
		return MissingReference
	}
	if p.Currency == string(solanapay.SOL) {
		if tx.RecipientDeltaLamports != p.AmountBase {
			return WrongAmount
		}
		return Valid
	}
	// USDC: token delta on the expected mint must match exactly; value on any
	// other mint means this is not the expected payment.
	delta, ok := tx.TokenDeltas[usdcMint]
	if !ok || delta == 0 {
		return WrongMint
	}
	if delta != p.AmountBase {
		return WrongAmount
	}
	for mint, d := range tx.TokenDeltas {
		if mint != usdcMint && d != 0 {
			return WrongMint
		}
	}
	return Valid
}

func containsKey(keys []string, want string) bool {
	for _, k := range keys {
		if k == want {
			return true
		}
	}
	return false
}
