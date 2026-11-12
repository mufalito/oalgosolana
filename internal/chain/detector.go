package chain

import (
	"context"
	"time"
)

// TxInfo is the normalized view of a transaction the validator needs.
// Deltas are per-recipient (the merchant wallet) balance changes.
type TxInfo struct {
	Signature              string
	RecipientDeltaLamports uint64            // SOL paid to recipient
	TokenDeltas            map[string]uint64 // mint -> units paid to recipient
	AccountKeys            []string          // all account pubkeys in the tx
}

// Detector abstracts the on-chain poll strategy (per-reference
// getSignaturesForAddress today; websockets or wallet-scanning later).
type Detector interface {
	// SignaturesForAddress returns candidate signatures touching ref.
	SignaturesForAddress(ctx context.Context, ref string) ([]string, error)
	// Transaction returns the parsed transaction or an error (incl. not found).
	// commitment is "confirmed" or "finalized".
	Transaction(ctx context.Context, signature, recipient, commitment string) (*TxInfo, error)
}

// Clock allows tests to freeze time.
type Clock func() time.Time

// RealClock is the production clock.
func RealClock() Clock { return func() time.Time { return time.Now().UTC() } }
