// Package chain wraps the Solana RPC client behind the Detector interface so
// the watcher never depends on RPC details, and provides a health probe.
package chain

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

var healthClient = &http.Client{Timeout: 2 * time.Second}

// HealthCheck calls getHealth on the RPC endpoint; nil means healthy.
func HealthCheck(ctx context.Context, rpcURL string) error {
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "getHealth",
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rpcURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := healthClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("getHealth: http %d", resp.StatusCode)
	}
	var out struct {
		Result string `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return err
	}
	if out.Result != "ok" {
		return fmt.Errorf("getHealth: %s", out.Result)
	}
	return nil
}
