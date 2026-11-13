package chain

import "context"

// Detector is the on-chain surface the watcher needs in vault mode:
// vault balances (trigger) and address signatures (receipt).
type Detector interface {
	// GetBalance returns the lamports balance of an address.
	GetBalance(ctx context.Context, address string) (uint64, error)
	// SignaturesForAddress returns recent successful signatures touching
	// an address (used to record the customer's payment as the receipt).
	SignaturesForAddress(ctx context.Context, address string) ([]string, error)
}
