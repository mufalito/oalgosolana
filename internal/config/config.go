// Package config loads and validates all gateway configuration from the
// environment. The merchant wallet and API keys are server-side config only
// and can never be set through the public API.
package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/gagliardetto/solana-go"
)

type Config struct {
	Port                  int      `env:"PORT" envDefault:"8080"`
	APIKeys               []string `env:"API_KEYS" envSeparator:","`
	MerchantWallet        solana.PublicKey
	MerchantWalletRaw     string        `env:"MERCHANT_WALLET"`
	Cluster               string        `env:"CLUSTER" envDefault:"devnet"`
	RPCURL                string        `env:"RPC_URL"`
	WebhookSecret         string        `env:"WEBHOOK_SECRET"`
	DatabaseURL           string        `env:"DATABASE_URL"`
	USDCMintMainnet       string        `env:"USDC_MINT_MAINNET" envDefault:"EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"`
	USDCMintDevnet        string        `env:"USDC_MINT_DEVNET" envDefault:""`
	ConfirmationPolicy    string        `env:"CONFIRMATION_POLICY" envDefault:"confirmed"`
	ExpirationDefaultMins int           `env:"EXPIRATION_MINUTES_DEFAULT" envDefault:"15"`
	ExpirationMinMins     int           `env:"EXPIRATION_MINUTES_MIN" envDefault:"1"`
	ExpirationMaxMins     int           `env:"EXPIRATION_MINUTES_MAX" envDefault:"1440"`
	WebhookMaxAttempts    int           `env:"WEBHOOK_MAX_ATTEMPTS" envDefault:"8"`
	PollInterval          time.Duration `env:"POLL_INTERVAL" envDefault:"5s"`
	WorkerCount           int           `env:"WORKER_COUNT" envDefault:"4"`
	RateLimitRPS          float64       `env:"RATE_LIMIT_RPS" envDefault:"10"`
	RateLimitBurst        int           `env:"RATE_LIMIT_BURST" envDefault:"20"`
	DepositAddressMode    bool          `env:"DEPOSIT_ADDRESS_MODE" envDefault:"false"`
	// Deposit mode uses on-chain vault program PDAs: funds can only ever
	// leave to the cold wallet (see vault-program/).
	DepositProgramID string `env:"DEPOSIT_PROGRAM_ID"`
	// GatewayHotWallet: path to a JSON keypair paying vault rent + fees
	// (vault backend only). It can create vaults and trigger sweeps but can
	// never redirect funds.
	GatewayHotWallet string `env:"GATEWAY_HOT_WALLET"`
}

// Load parses the environment and validates invariants. It returns an error
// listing every violated requirement.
func Load() (*Config, error) {
	var cfg Config
	if err := env.Parse(&cfg); err != nil {
		return nil, fmt.Errorf("parse env: %w", err)
	}
	var errs []string
	if len(cfg.APIKeys) == 0 {
		errs = append(errs, "API_KEYS: at least one API key is required")
	}
	for i, k := range cfg.APIKeys {
		if len(k) < 16 {
			errs = append(errs, fmt.Sprintf("API_KEYS[%d]: must be at least 16 characters", i))
		}
	}
	wallet, err := solana.PublicKeyFromBase58(cfg.MerchantWalletRaw)
	if err != nil {
		errs = append(errs, "MERCHANT_WALLET: invalid base58 pubkey: "+err.Error())
	} else if wallet.Equals(solana.PublicKey{}) {
		errs = append(errs, "MERCHANT_WALLET: must not be the zero pubkey")
	}
	if cfg.MerchantWalletRaw != "" {
		cfg.MerchantWallet = wallet
	}
	if cfg.RPCURL == "" {
		errs = append(errs, "RPC_URL: required (never rely on a public mainnet RPC)")
	}
	if len(cfg.WebhookSecret) < 32 {
		errs = append(errs, "WEBHOOK_SECRET: must be at least 32 bytes")
	}
	if cfg.DatabaseURL == "" {
		errs = append(errs, "DATABASE_URL: required")
	}
	if cfg.ExpirationMinMins < 1 || cfg.ExpirationMaxMins < cfg.ExpirationMinMins {
		errs = append(errs, "EXPIRATION_MINUTES_MIN/MAX: invalid bounds")
	}
	if cfg.ExpirationDefaultMins < cfg.ExpirationMinMins || cfg.ExpirationDefaultMins > cfg.ExpirationMaxMins {
		errs = append(errs, "EXPIRATION_MINUTES_DEFAULT: outside configured min/max")
	}
	if cfg.WebhookMaxAttempts < 1 {
		errs = append(errs, "WEBHOOK_MAX_ATTEMPTS: must be >= 1")
	}
	if cfg.WorkerCount < 1 {
		errs = append(errs, "WORKER_COUNT: must be >= 1")
	}
	if _, err := solana.PublicKeyFromBase58(cfg.DepositProgramID); err != nil {
		errs = append(errs, "DEPOSIT_PROGRAM_ID: invalid base58 pubkey (required)")
	}
	if cfg.GatewayHotWallet == "" {
		errs = append(errs, "GATEWAY_HOT_WALLET: keypair path required")
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("invalid configuration:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return &cfg, nil
}
