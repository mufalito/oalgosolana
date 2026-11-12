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
	"github.com/mufalito/oalgosolana/internal/wallet"
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
	// VaultCreator creates on-chain deposit vaults (vault backend only).
	VaultCreator VaultCreator
}

// VaultCreator is implemented by vault.Sweeper.
type VaultCreator interface {
	CreateVault(ctx context.Context, seed solana.PublicKey) (solana.PublicKey, error)
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
	var depositSecret []byte
	var vaultAddr string
	if s.Cfg.DepositAddressMode {
		// Deposit address is a vault-program PDA whose funds can only leave
		// to the cold wallet. The stored "secret" is just the 32-byte seed
		// (no private key exists).
		seed, err := solanapay.NewReference()
		if err != nil {
			return nil, apierr.Errorf(500, apierr.CodeInternal, "generate vault seed")
		}
		addr, err := s.VaultCreator.CreateVault(ctx, seed)
		if err != nil {
			s.Log.ErrorContext(ctx, "vault creation failed", "err", err.Error())
			return nil, apierr.Errorf(500, apierr.CodeInternal, "create deposit vault")
		}
		vaultAddr = addr.String()
		reference = addr
		depositSecret, err = wallet.EncryptSecret(seed.Bytes(), s.depositMasterKey())
		if err != nil {
			return nil, apierr.Errorf(500, apierr.CodeInternal, "encrypt vault seed")
		}
	}
	id := uuid.Must(uuid.NewV7())
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
	if depositSecret != nil {
		if err := s.Store.CreateDepositKey(ctx, &store.DepositKeyRow{
			PaymentID: p.ID, PublicKey: p.Reference, EncryptedSecret: depositSecret,
		}); err != nil {
			return nil, apierr.Errorf(500, apierr.CodeInternal, "persist deposit key")
		}
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

func (s *Service) depositMasterKey() []byte {
	raw, err := base64.StdEncoding.DecodeString(s.Cfg.DepositMasterKeyRaw)
	if err != nil || len(raw) != 32 {
		return nil
	}
	return raw
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
