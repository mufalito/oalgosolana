package chain

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
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
	if string(envelope.Result) == "null" {
		return &NotFoundError{Method: method}
	}
	if err := json.Unmarshal(envelope.Result, result); err != nil {
		return fmt.Errorf("%s: decode result: %w", method, err)
	}
	return nil
}

// NotFoundError means the RPC returned null for the requested object
// (e.g. getTransaction before indexing) — callers should retry.
type NotFoundError struct{ Method string }

func (e *NotFoundError) Error() string { return e.Method + ": not found" }

// IsNotFound reports whether err is a NotFoundError.
func IsNotFound(err error) bool {
	var nf *NotFoundError
	return errors.As(err, &nf)
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

// accountKey accepts both plain base58 strings and objects
// ({"pubkey": "...", ...}) that some RPC providers return for versioned
// messages with address-lookup tables.
type accountKey string

func (k *accountKey) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*k = accountKey(s)
		return nil
	}
	var obj struct {
		Pubkey string `json:"pubkey"`
	}
	if err := json.Unmarshal(b, &obj); err != nil {
		return err
	}
	*k = accountKey(obj.Pubkey)
	return nil
}

func (k accountKey) String() string { return string(k) }

type jsonParsedTx struct {
	Transaction struct {
		Message struct {
			AccountKeys []accountKey `json:"accountKeys"`
		} `json:"message"`
	} `json:"transaction"`
	Meta *struct {
		PreBalances       []uint64       `json:"preBalances"`
		PostBalances      []uint64       `json:"postBalances"`
		PreTokenBalances  []tokenBalance `json:"preTokenBalances"`
		PostTokenBalances []tokenBalance `json:"postTokenBalances"`
		Err               any            `json:"err"`
	} `json:"meta"`
	Slot uint64 `json:"slot"`
}

type tokenBalance struct {
	AccountIndex int    `json:"accountIndex"`
	Mint         string `json:"mint"`
	Owner        string `json:"owner"`
	Amount       string `json:"amount"`
}

// Transaction fetches a jsonParsed transaction and computes per-recipient
// deltas for the given wallet (merchant) as TxInfo.
func (d *RPCDetector) Transaction(ctx context.Context, signature, recipient, commitment string) (*TxInfo, error) {
	var tx jsonParsedTx
	if err := d.call(ctx, &tx, "getTransaction", signature,
		map[string]any{"encoding": "jsonParsed", "maxSupportedTransactionVersion": 0, "commitment": commitment}); err != nil {
		return nil, err
	}
	if tx.Meta == nil || tx.Transaction.Message.AccountKeys == nil {
		return nil, &NotFoundError{Method: "getTransaction"}
	}
	if tx.Meta.Err != nil {
		return nil, fmt.Errorf("transaction %s failed on-chain", signature)
	}
	keys := make([]string, len(tx.Transaction.Message.AccountKeys))
	for i, k := range tx.Transaction.Message.AccountKeys {
		keys[i] = k.String()
	}
	info := &TxInfo{Signature: signature, AccountKeys: keys, TokenDeltas: map[string]uint64{}}
	// SOL: lamport delta of the recipient's account entry.
	for i, k := range keys {
		if k == recipient && i < len(tx.Meta.PostBalances) && i < len(tx.Meta.PreBalances) &&
			tx.Meta.PostBalances[i] > tx.Meta.PreBalances[i] {
			info.RecipientDeltaLamports = tx.Meta.PostBalances[i] - tx.Meta.PreBalances[i]
		}
	}
	// Tokens: net positive balance delta per mint for token accounts owned by
	// the recipient (account indexes are stable pre/post within one tx).
	preAmt := map[int]uint64{}
	for _, b := range tx.Meta.PreTokenBalances {
		preAmt[b.AccountIndex] = parseAmountUint(b.Amount)
	}
	for _, b := range tx.Meta.PostTokenBalances {
		if b.Owner != recipient {
			continue
		}
		post := parseAmountUint(b.Amount)
		pre := preAmt[b.AccountIndex]
		if post >= pre {
			info.TokenDeltas[b.Mint] += post - pre
		}
	}
	return info, nil
}

func parseAmountUint(s string) uint64 {
	var v uint64
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		v = v*10 + uint64(r-'0')
	}
	return v
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
