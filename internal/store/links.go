package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// PaymentLink is a reusable, server-side payment link.
type PaymentLink struct {
	ID                string
	AmountBase        uint64
	Decimals          int
	Currency          string
	Label             string
	Message           string
	ExpirationMinutes int
	LinkExpiresAt     *time.Time
	CreatedAt         time.Time
}

// ErrLinkNotFound / ErrLinkExpired.
var (
	ErrLinkNotFound = errors.New("payment link not found")
	ErrLinkExpired  = errors.New("payment link expired")
)

// CreatePaymentLink stores a payment link.
func (s *Store) CreatePaymentLink(ctx context.Context, l *PaymentLink) error {
	_, err := s.Pool.Exec(ctx,
		`INSERT INTO payment_links (id, amount_base, decimals, currency, label, message,
		   expiration_minutes, link_expires_at, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		l.ID, l.AmountBase, l.Decimals, l.Currency, l.Label, l.Message,
		l.ExpirationMinutes, l.LinkExpiresAt, l.CreatedAt)
	return err
}

// GetPaymentLink fetches a link and enforces its expiry.
func (s *Store) GetPaymentLink(ctx context.Context, id string) (*PaymentLink, error) {
	var l PaymentLink
	err := s.Pool.QueryRow(ctx,
		`SELECT id, amount_base, decimals, currency, label, message,
		   expiration_minutes, link_expires_at, created_at
		 FROM payment_links WHERE id = $1`, id).
		Scan(&l.ID, &l.AmountBase, &l.Decimals, &l.Currency, &l.Label, &l.Message,
			&l.ExpirationMinutes, &l.LinkExpiresAt, &l.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrLinkNotFound
	}
	if err != nil {
		return nil, err
	}
	if l.LinkExpiresAt != nil && time.Now().UTC().After(*l.LinkExpiresAt) {
		return nil, ErrLinkExpired
	}
	return &l, nil
}
