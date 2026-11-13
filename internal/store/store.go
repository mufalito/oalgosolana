// Package store provides the pgxpool connection, embedded goose migrations,
// and the payment / webhook-delivery row types shared across packages.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"time"

	"github.com/mufalito/oalgosolana/migrations"
	"github.com/pressly/goose/v3"
)

// Store wraps the pool with repository methods.
type Store struct {
	Pool *pgxpool.Pool
}

// Open creates a pgxpool and runs all embedded migrations.
func Open(ctx context.Context, databaseURL string) (*Store, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	// goose runs over database/sql (pgx stdlib driver); the pool serves the app.
	sqlDB, err := sql.Open("pgx", databaseURL)
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("open migration connection: %w", err)
	}
	defer sqlDB.Close()
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		pool.Close()
		return nil, fmt.Errorf("goose dialect: %w", err)
	}
	if err := goose.UpContext(ctx, sqlDB, "."); err != nil {
		pool.Close()
		return nil, fmt.Errorf("run migrations: %w", err)
	}
	goose.SetBaseFS(nil)
	return &Store{Pool: pool}, nil
}

// Payment is the on-disk row shape shared across packages.
type Payment struct {
	ID             string
	OrderID        string
	AmountBase     uint64
	Decimals       int
	Currency       string
	Status         string
	Reference      string
	MerchantWallet string
	Label          string
	Message        string
	WebhookURL     string
	ExpiresAt      time.Time
	CreatedAt      time.Time
	TxSignature    string
	DetectedTx     string
	SweepTx        string
	ConfirmedAt    *time.Time
	FinalizedAt    *time.Time
	FailureReason  string
}

// WebhookDelivery is one queued event delivery (at-least-once per event type).
type WebhookDelivery struct {
	ID            int64
	PaymentID     string
	EventType     string
	Payload       []byte
	Status        string
	Attempts      int
	NextAttemptAt time.Time
	LastError     string
	SentAt        *time.Time
}

// IsUniqueViolation reports a Postgres unique-constraint violation (SQLSTATE 23505).
func IsUniqueViolation(err error) bool {
	var pgErr interface{ SQLState() string }
	if errors.As(err, &pgErr) {
		return pgErr.SQLState() == "23505"
	}
	return false
}

// Close releases the pool.
func (s *Store) Close() { s.Pool.Close() }
