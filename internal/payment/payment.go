// Package payment implements the domain service for payment creation and
// status retrieval. The merchant wallet is server config; it is never accepted
// from API requests.
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
	// OnCreated is an optional metrics hook (currency label).
	OnCreated func(currency string)
	// DepositProgramID + DepositRentMinimum are set in deposit mode.
	// RentMinimum is read from RPC (rent changes with protocol upgrades);
	// the first transfer to a fresh vault address must cover it.
	DepositProgramID   solana.PublicKey
	DepositRentMinimum func(ctx context.Context) uint64
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
	Reference      string `json:"reference"`
	Status         string `json:"status"`
	DepositAddress string `json:"deposit_address,omitempty"`
	ExpiresAt      string `json:"expires_at"`
}

// Get is the response payload of GET /api/v1/payments/{id}.
type Get struct {
	PaymentID     string            `json:"payment_id"`
	OrderID       string            `json:"order_id"`
	Amount        string            `json:"amount"`
	AmountBase    string            `json:"amount_base"`
	Currency      string            `json:"currency"`
	Status        string            `json:"status"`
	SolanaPayURL  string            `json:"solana_pay_url,omitempty"`
	Reference     string            `json:"reference"`
	TxSignature   string            `json:"tx_signature,omitempty"`
	DetectedTx    string            `json:"detected_tx,omitempty"`
	ConfirmedAt   string            `json:"confirmed_at,omitempty"`
	FinalizedAt   string            `json:"finalized_at,omitempty"`
	ExpiresAt     string            `json:"expires_at"`
	FailureReason string            `json:"failure_reason,omitempty"`
	Webhook       map[string]string `json:"webhook,omitempty"`
}

// Create validates input, generates reference/UUIDv7, persists, and returns
// the Solana Pay URL and QR code.
func (s *Service) Create(ctx context.Context, req CreateRequest) (*Created, error) {
	cur := solanapay.Currency(req.Currency)
	if cur != solanapay.SOL && cur != solanapay.USDC {
		return nil, apierr.Errorf(400, apierr.CodeInvalidCurrency, "currency must be SOL or USDC")
	}
	if cur == solanapay.USDC && s.Cfg.USDCMint() == "" {
		return nil, apierr.Errorf(400, apierr.CodeInvalidCurrency, "USDC mint not configured for cluster")
	}
	if s.Cfg.DepositAddressMode && cur == solanapay.USDC {
		return nil, apierr.Errorf(400, apierr.CodeInvalidCurrency, "deposit-address mode supports SOL only")
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
	amountBase, err := solanapay.ParseAmount(req.Amount, cur)
	if err != nil {
		return nil, apierr.Errorf(400, apierr.CodeInvalidAmount, "amount: %v", err)
	}

	reference, err := solanapay.NewReference()
	if err != nil {
		return nil, apierr.Errorf(500, apierr.CodeInternal, "generate reference")
	}
	id := uuid.Must(uuid.NewV7())
	var vaultAddr string
	if s.Cfg.DepositAddressMode {
		// Deposit address = vault PDA derived from [merchant, invoice id].
		// Nothing is created on-chain at invoice time: the customer's own
		// transfer creates the vault (and pays its rent); the sweep drains
		// it entirely. No secrets, no keys, no create transaction.
		if s.DepositRentMinimum != nil {
			if min := s.DepositRentMinimum(ctx); amountBase < min {
				return nil, apierr.Errorf(400, apierr.CodeInvalidAmount,
					"deposit-address mode requires at least %s SOL (rent-exempt minimum read from RPC)",
					solanapay.FormatAmount(min, solanapay.SOL))
			}
		}
		addr, err := vault.Address(s.Cfg.MerchantWallet, id.String(), s.DepositProgramID)
		if err != nil {
			return nil, apierr.Errorf(500, apierr.CodeInternal, "derive vault address")
		}
		vaultAddr = addr.String()
		reference = addr
	}
	now := time.Now().UTC()
	p := &store.Payment{
		ID:             id.String(),
		OrderID:        req.OrderID,
		AmountBase:     amountBase,
		Currency:       req.Currency,
		Status:         "pending",
		Reference:      reference.String(),
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
	mint := ""
	if cur == solanapay.USDC {
		mint = s.Cfg.USDCMint()
	}
	// Deposit mode: customer pays the deposit address directly (any wallet,
	// no Solana Pay support needed). Reference mode: merchant wallet + ref key.
	recipient := s.Cfg.MerchantWallet
	if vaultAddr != "" {
		if vp, err := solana.PublicKeyFromBase58(vaultAddr); err == nil {
			recipient = vp
		}
	}
	payURL := solanapay.BuildURL(recipient, amountBase, cur, mint, reference, req.Label, req.Message)
	qr, err := solanapay.QRCodePNG(payURL)
	if err != nil {
		return nil, apierr.Errorf(500, apierr.CodeInternal, "encode QR")
	}
	s.Log.InfoContext(ctx, "payment_created",
		"payment_id", p.ID, "reference", p.Reference, "order_id", p.OrderID,
		"currency", p.Currency, "amount_base", p.AmountBase)
	if s.OnCreated != nil {
		s.OnCreated(p.Currency)
	}
	resp := &Created{
		PaymentID:    p.ID,
		OrderID:      p.OrderID,
		Amount:       solanapay.FormatAmount(amountBase, cur),
		AmountBase:   uitoa(amountBase),
		Currency:     p.Currency,
		SolanaPayURL: payURL,
		QRCode:       base64.StdEncoding.EncodeToString(qr),
		Reference:    p.Reference,
		Status:       p.Status,
		ExpiresAt:    p.ExpiresAt.Format(time.RFC3339),
	}
	if vaultAddr != "" {
		resp.DepositAddress = vaultAddr
	}
	return resp, nil
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
		PaymentID:     p.ID,
		OrderID:       p.OrderID,
		Amount:        solanapay.FormatAmount(p.AmountBase, solanapay.Currency(p.Currency)),
		AmountBase:    uitoa(p.AmountBase),
		Currency:      p.Currency,
		Status:        p.Status,
		Reference:     p.Reference,
		TxSignature:   p.TxSignature,
		DetectedTx:    p.DetectedTx,
		ExpiresAt:     p.ExpiresAt.Format(time.RFC3339),
		FailureReason: p.FailureReason,
	}
	if p.ConfirmedAt != nil {
		g.ConfirmedAt = p.ConfirmedAt.Format(time.RFC3339)
	}
	if p.FinalizedAt != nil {
		g.FinalizedAt = p.FinalizedAt.Format(time.RFC3339)
	}
	statuses, err := s.Store.WebhookStatuses(ctx, p.ID)
	if err != nil {
		return nil, apierr.Errorf(500, apierr.CodeInternal, "fetch webhook statuses")
	}
	if p.WebhookURL != "" {
		if statuses == nil {
			statuses = map[string]string{}
		}
		g.Webhook = statuses
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
