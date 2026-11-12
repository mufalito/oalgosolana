// Package apierr defines the machine-readable API error envelope shared by
// the api handlers and the payment domain service (leaf package: no deps).
package apierr

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// Error codes returned by the API.
const (
	CodeUnauthorized    = "UNAUTHORIZED"
	CodeRateLimited     = "RATE_LIMITED"
	CodeNotFound        = "NOT_FOUND"
	CodeInvalidAmount   = "INVALID_AMOUNT"
	CodeInvalidCurrency = "INVALID_CURRENCY"
	CodeInvalidRequest  = "INVALID_REQUEST"
	CodeInvalidWebhook  = "INVALID_WEBHOOK_URL"
	CodeInvalidExpiry   = "INVALID_EXPIRATION"
	CodeDuplicateOrder  = "DUPLICATE_ORDER_ID"
	CodeInternal        = "INTERNAL"
)

// APIError carries an HTTP status and machine-readable code.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string { return e.Code + ": " + e.Message }

// Errorf builds an *APIError.
func Errorf(status int, code, format string, args ...any) *APIError {
	return &APIError{Status: status, Code: code, Message: fmt.Sprintf(format, args...)}
}

type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// WriteError writes the error envelope. Non-APIError values become 500 INTERNAL.
func WriteError(w http.ResponseWriter, err error) {
	var ae *APIError
	if !errors.As(err, &ae) {
		ae = &APIError{Status: http.StatusInternalServerError, Code: CodeInternal, Message: "internal server error"}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(ae.Status)
	json.NewEncoder(w).Encode(errorBody{Error: struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{Code: ae.Code, Message: ae.Message}})
}
