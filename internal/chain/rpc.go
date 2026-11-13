package chain

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"golang.org/x/time/rate"
)

// RPCDetector polls a Solana JSON-RPC endpoint. It uses raw getTransaction
// with jsonParsed encoding and reads meta.pre/post balance fields directly
// (no hand-decoding of SPL instruction data).
type RPCDetector struct {
	URL     string
	Client  *http.Client
	Limiter *rate.Limiter
	// OnRequest / OnError are optional metrics hooks (method label).
	OnRequest func(method string, d time.Duration)
	OnError   func(method string)
}

// NewRPCDetector builds a detector with a 10s timeout and per-RPC limiter.
func NewRPCDetector(url string, rps float64) *RPCDetector {
	return &RPCDetector{
		URL:     url,
		Client:  &http.Client{Timeout: 10 * time.Second},
		Limiter: rate.NewLimiter(rate.Limit(rps), 10),
	}
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  []any  `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message) }

func (d *RPCDetector) call(ctx context.Context, result any, method string, params ...any) error {
	if err := d.Limiter.Wait(ctx); err != nil {
		return err
	}
	start := time.Now()
	body, _ := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: 1, Method: method, Params: params})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.Client.Do(req)
	if d.OnRequest != nil {
		d.OnRequest(method, time.Since(start))
	}
	if err != nil {
		if d.OnError != nil {
			d.OnError(method)
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if d.OnError != nil {
			d.OnError(method)
		}
		return fmt.Errorf("%s: http %d", method, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		if d.OnError != nil {
			d.OnError(method)
		}
		return err
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  *rpcError       `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		if d.OnError != nil {
			d.OnError(method)
		}
		return fmt.Errorf("%s: decode envelope: %w", method, err)
	}
	if envelope.Error != nil {
		if d.OnError != nil {
			d.OnError(method)
		}
		return envelope.Error
	}
	if err := json.Unmarshal(envelope.Result, result); err != nil {
		return fmt.Errorf("%s: decode result: %w", method, err)
	}
	return nil
}

// SignaturesForAddress returns up to 10 recent successful signatures touching ref.
func (d *RPCDetector) SignaturesForAddress(ctx context.Context, ref string) ([]string, error) {
	var sigs []struct {
		Signature string `json:"signature"`
		Err       *struct {
			Message string `json:"message"`
		} `json:"err"`
	}
	if err := d.call(ctx, &sigs, "getSignaturesForAddress", ref, map[string]any{"limit": 10}); err != nil {
		return nil, err
	}
	var out []string
	for _, s := range sigs {
		if s.Err == nil {
			out = append(out, s.Signature)
		}
	}
	return out, nil
}

// GetLatestBlockhash returns a recent blockhash for building transactions.
func (d *RPCDetector) GetLatestBlockhash(ctx context.Context) (string, error) {
	var out struct {
		Value struct {
			Blockhash string `json:"blockhash"`
		} `json:"value"`
	}
	if err := d.call(ctx, &out, "getLatestBlockhash", map[string]any{"commitment": "confirmed"}); err != nil {
		return "", err
	}
	return out.Value.Blockhash, nil
}

// SendTransaction submits a signed transaction (base64 wire format).
func (d *RPCDetector) SendTransaction(ctx context.Context, txBytes []byte) (string, error) {
	var sig string
	if err := d.call(ctx, &sig, "sendTransaction",
		base64.StdEncoding.EncodeToString(txBytes),
		map[string]any{"encoding": "base64", "skipPreflight": true}); err != nil {
		return "", err
	}
	return sig, nil
}

// GetBalance returns the lamports balance of an address.
func (d *RPCDetector) GetBalance(ctx context.Context, address string) (uint64, error) {
	var out struct {
		Value uint64 `json:"value"`
	}
	if err := d.call(ctx, &out, "getBalance", address, map[string]any{"commitment": "confirmed"}); err != nil {
		return 0, err
	}
	return out.Value, nil
}

// SignatureStatus is the parsed status of one signature (Err == nil means
// the transaction succeeded on-chain).
type SignatureStatus struct {
	Slot               uint64 `json:"slot"`
	ConfirmationStatus string `json:"confirmationStatus"`
	Err                any    `json:"err"`
}

// GetSignatureStatuses returns per-signature status (nil status = unknown).
func (d *RPCDetector) GetSignatureStatuses(ctx context.Context, sigs []string) ([]*SignatureStatus, error) {
	var out struct {
		Value []*SignatureStatus `json:"value"`
	}
	list := make([]string, len(sigs))
	for i, s := range sigs {
		list[i] = s
	}
	if err := d.call(ctx, &out, "getSignatureStatuses", list, map[string]any{"searchTransactionHistory": false}); err != nil {
		return nil, err
	}
	return out.Value, nil
}

// RentExemptMinimum returns the rent-exempt minimum for an empty (0-byte)
// account, read live from RPC so rent changes (stepped cuts) are followed.
func (d *RPCDetector) RentExemptMinimum(ctx context.Context) uint64 {
	var out struct {
		Value uint64 `json:"value"`
	}
	if err := d.call(ctx, &out, "getMinimumBalanceForRentExemptSize", 0); err != nil {
		// conservative fallback: the long-standing pre-cut minimum
		return 890880
	}
	return out.Value
}
