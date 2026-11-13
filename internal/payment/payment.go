// Package payment implements the domain service for vault-mode payment
// creation and status retrieval. Every invoice's deposit address is a
// derived vault PDA; nothing touches the chain at creation time.
package payment

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/url"
	"time"

	"github.com/gagliardetto/solana-go"
	"github.com/google/uuid"

	"github.com/mufalito/oalgosolana/internal/apierr"
	"github.com/mufalito/oalgosolana/internal/config"
	"github.com/mufalito/oalgosolana/internal/solanapay"
	"github.com/mufalito/oalgosolana/internal/store"
	"github.com/mufalito/oalgosolana/internal/vault"
)

// CreateRequest is the validated input of POST /api/v1/payments.
type CreateRequest struct {
	Amount           string
	Currency         string
	OrderID          string
	Label            string
	Message          string
	WebhookURL       string
	ExpirationMinSet bool
	ExpirationMin    int
}

// Service creates and reads payments.
type Service struct {
	Store *store.Store
	Cfg   *config.Config
	Log   *slog.Logger
	// OnCreated is an optional metrics hook.
	OnCreated          func(currency string)
	DepositProgramID   solana.PublicKey
	DepositRentMinimum func(ctx context.Context) uint64
	// MintDecimals fetches an SPL mint's decimals (nil = SOL-only).
	MintDecimals func(ctx context.Context, mint string) (uint8, error)
}

// Created is the response payload of payment creation.
type Created struct {
	PaymentID      string `json:"payment_id"`
	OrderID        string `json:"order_id"`
	Amount         string `json:"amount"`
	AmountBase     string `json:"amount_base"`
	Currency       string `json:"currency"`
	SolanaPayURL   string `json:"solana_pay_url"`
	QRCode         string `json:"qr_code"`
	DepositAddress string `json:"deposit_address"`
	Status         string `json:"status"`
	ExpiresAt      string `json:"expires_at"`
}

// Get is the response payload of GET /api/v1/payments/{id}.
type Get struct {
	PaymentID      string `json:"payment_id"`
	OrderID        string `json:"order_id"`
	Amount         string `json:"amount"`
	AmountBase     string `json:"amount_base"`
	Currency       string `json:"currency"`
	Status         string `json:"status"`
	DepositAddress string `json:"deposit_address"`
	TxSignature    string `json:"tx_signature,omitempty"`
	ConfirmedAt    string `json:"confirmed_at,omitempty"`
	FinalizedAt    string `json:"finalized_at,omitempty"`
	ExpiresAt      string `json:"expires_at"`
}

// Create validates input, derives the vault address, persists, and returns
// the payment URL and QR code. No on-chain transaction is made.
func (s *Service) Create(ctx context.Context, req CreateRequest) (*Created, error) {
	currency := "SOL"
	decimals := solanapay.SOLDecimals
	mint := ""
	if req.Currency != "" && req.Currency != "SOL" {
		if _, err := solana.PublicKeyFromBase58(req.Currency); err != nil {
			return nil, apierr.Errorf(400, apierr.CodeInvalidCurrency, "currency must be SOL or a base58 mint address")
		}
		if !s.Cfg.AcceptsMint(req.Currency) {
			return nil, apierr.Errorf(400, apierr.CodeInvalidCurrency, "mint not accepted by this gateway")
		}
		if s.MintDecimals == nil {
			return nil, apierr.Errorf(500, apierr.CodeInternal, "token support not configured")
		}
		d, err := s.MintDecimals(ctx, req.Currency)
		if err != nil {
			return nil, apierr.Errorf(400, apierr.CodeInvalidCurrency, "unknown or non-mint account")
		}
		mint = req.Currency
		currency = req.Currency
		decimals = int(d)
	}
	if req.OrderID == "" {
		return nil, apierr.Errorf(400, apierr.CodeInvalidRequest, "order_id is required")
	}
	if req.WebhookURL != "" {
		u, err := url.Parse(req.WebhookURL)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return nil, apierr.Errorf(400, apierr.CodeInvalidWebhook, "webhook_url must be an https:// URL")
		}
	}
	expirationMin := s.Cfg.ExpirationDefaultMins
	if req.ExpirationMinSet {
		if req.ExpirationMin < s.Cfg.ExpirationMinMins || req.ExpirationMin > s.Cfg.ExpirationMaxMins {
			return nil, apierr.Errorf(400, apierr.CodeInvalidExpiry,
				"expiration_minutes must be between %d and %d", s.Cfg.ExpirationMinMins, s.Cfg.ExpirationMaxMins)
		}
		expirationMin = req.ExpirationMin
	}
	amountBase, err := solanapay.ParseAmount(req.Amount, decimals)
	if err != nil {
		return nil, apierr.Errorf(400, apierr.CodeInvalidAmount, "amount: %v", err)
	}
	// A SOL vault address is created by the customer's first transfer,
	// which must leave the new account rent-exempt — the invoice has to
	// allow it. Token vaults need no SOL minimum (the payer's wallet also
	// creates the vault's token account).
	if mint == "" && s.DepositRentMinimum != nil {
		if min := s.DepositRentMinimum(ctx); amountBase < min {
			return nil, apierr.Errorf(400, apierr.CodeInvalidAmount,
				"amount must be at least %s SOL (rent-exempt minimum read from RPC)",
				solanapay.FormatAmount(min, solanapay.SOLDecimals))
		}
	}

	id := uuid.Must(uuid.NewV7())
	// Deposit address = vault PDA derived from [merchant, invoice id].
	// Nothing is created on-chain at invoice time: the customer's own
	// transfer creates the vault (and pays its rent); the sweep drains it
	// entirely. No secrets, no keys, no create transaction.
	addr, err := vault.Address(s.Cfg.MerchantWallet, id.String(), s.DepositProgramID)
	if err != nil {
		return nil, apierr.Errorf(500, apierr.CodeInternal, "derive vault address")
	}
	now := time.Now().UTC()
	p := &store.Payment{
		ID:             id.String(),
		OrderID:        req.OrderID,
		AmountBase:     amountBase,
		Decimals:       decimals,
		Currency:       currency,
		Status:         "pending",
		Reference:      addr.String(), // vault address
		MerchantWallet: s.Cfg.MerchantWallet.String(),
		Label:          req.Label,
		Message:        req.Message,
		WebhookURL:     req.WebhookURL,
		ExpiresAt:      now.Add(time.Duration(expirationMin) * time.Minute),
		CreatedAt:      now,
	}
	if err := s.Store.CreatePayment(ctx, p); err != nil {
		if store.IsUniqueViolation(err) {
			return nil, apierr.Errorf(409, apierr.CodeDuplicateOrder, "order_id already exists")
		}
		return nil, apierr.Errorf(500, apierr.CodeInternal, "persist payment")
	}
	payURL := solanapay.BuildURL(addr.String(), req.Label, req.Message, amountBase, decimals, mint)
	qr, err := solanapay.QRCodePNG(payURL)
	if err != nil {
		return nil, apierr.Errorf(500, apierr.CodeInternal, "encode QR")
	}
	if s.OnCreated != nil {
		s.OnCreated(currency)
	}
	s.Log.InfoContext(ctx, "payment_created",
		"payment_id", p.ID, "reference", p.Reference, "order_id", p.OrderID,
		"amount_base", p.AmountBase)
	return &Created{
		PaymentID:      p.ID,
		OrderID:        p.OrderID,
		Amount:         solanapay.FormatAmount(amountBase, decimals),
		AmountBase:     uitoa(amountBase),
		Currency:       currency,
		SolanaPayURL:   payURL,
		QRCode:         base64.StdEncoding.EncodeToString(qr),
		DepositAddress: addr.String(),
		Status:         p.Status,
		ExpiresAt:      p.ExpiresAt.Format(time.RFC3339),
	}, nil
}

// ListRecent returns the most recent payments (newest first).
func (s *Service) ListRecent(ctx context.Context) ([]*Get, error) {
	rows, err := s.Store.QueryPayments(ctx,
		`SELECT `+store.PaymentCols+` FROM payments ORDER BY created_at DESC LIMIT 50`)
	if err != nil {
		return nil, apierr.Errorf(500, apierr.CodeInternal, "list payments")
	}
	out := make([]*Get, 0, len(rows))
	for _, p := range rows {
		g := &Get{
			PaymentID:      p.ID,
			OrderID:        p.OrderID,
			Amount:         solanapay.FormatAmount(p.AmountBase, p.Decimals),
			AmountBase:     uitoa(p.AmountBase),
			Currency:       p.Currency,
			Status:         p.Status,
			DepositAddress: p.Reference,
			TxSignature:    p.TxSignature,
			ExpiresAt:      p.ExpiresAt.Format(time.RFC3339),
		}
		if p.ConfirmedAt != nil {
			g.ConfirmedAt = p.ConfirmedAt.Format(time.RFC3339)
		}
		if p.FinalizedAt != nil {
			g.FinalizedAt = p.FinalizedAt.Format(time.RFC3339)
		}
		out = append(out, g)
	}
	return out, nil
}

// GetByID fetches a payment; store.ErrNotFound maps to 404.
func (s *Service) GetByID(ctx context.Context, id string) (*Get, error) {
	uid, err := uuid.Parse(id)
	if err != nil {
		return nil, apierr.Errorf(404, apierr.CodeNotFound, "unknown payment id")
	}
	p, err := s.Store.GetPayment(ctx, uid)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, apierr.Errorf(404, apierr.CodeNotFound, "unknown payment id")
		}
		return nil, apierr.Errorf(500, apierr.CodeInternal, "fetch payment")
	}
	g := &Get{
		PaymentID:      p.ID,
		OrderID:        p.OrderID,
		Amount:         solanapay.FormatAmount(p.AmountBase, p.Decimals),
		AmountBase:     uitoa(p.AmountBase),
		Currency:       p.Currency,
		Status:         p.Status,
		DepositAddress: p.Reference,
		TxSignature:    p.TxSignature,
		ExpiresAt:      p.ExpiresAt.Format(time.RFC3339),
	}
	if p.ConfirmedAt != nil {
		g.ConfirmedAt = p.ConfirmedAt.Format(time.RFC3339)
	}
	if p.FinalizedAt != nil {
		g.FinalizedAt = p.FinalizedAt.Format(time.RFC3339)
	}
	return g, nil
}

func uitoa(n uint64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
