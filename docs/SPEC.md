# Build a Self-Hosted, Non-Custodial Solana Payment Gateway in Go

## Core Philosophy

- Fully self-hosted: no third party can switch us off. The ONLY external dependency is a Solana RPC endpoint, configurable via env (self-hosted node, or a paid provider like Helius/QuickNode — never public mainnet RPC in production).
- Non-custodial: funds move directly from the customer's wallet to the merchant's wallet. The API process holds ZERO private keys and can never move or sign anything.
- No KYC/KYB required to run.
- Security claim, stated precisely: a full server compromise cannot redirect funds already received; redirecting future payments would require changing the merchant wallet in server config — which is not settable via the public API and is audit-logged.
- Clean REST API usable by any website or coding agent.
- Production-ready security and reliability from day one.

## Scope

- Solana only: SOL + USDC (SPL token transfers).
- Native Solana Pay transfer requests (`solana:` URLs).
- Mainnet + devnet, selected via env config.
- Refunds and withdrawals are explicitly OUT of scope — merchants sign payouts manually from their own wallet. This API will never expose key-management or fund-moving endpoints.

## Money & Units (non-negotiable rules)

- SOL has 9 decimals, USDC has 6. Store and compare all amounts as `uint64` base units (lamports / smallest token unit).
- NEVER use float64 for money. Parse decimal strings at the API edge only, using shopspring/decimal or manual fixed-point parsing, then convert to uint64.
- API accepts `amount` as a decimal string ("1.50"); reject more decimals than the currency allows.
- USDC mint addresses are env-configurable with sane defaults (mainnet `EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v`; Circle devnet USDC — verify the value at setup).

## API (v1, single-merchant)

Authentication: Bearer API key (env-configured) on all endpoints; per-merchant keys come with multi-merchant support later. Rate limiting: token bucket per API key. Errors are machine-readable: `{"error": {"code": "...", "message": "..."}}`.

1. **POST /api/v1/payments** — create payment
   - Input: `amount` (decimal string), `currency` (SOL|USDC), `order_id` (merchant's own id, unique — duplicate returns 409), `label`, `message`, `webhook_url` (optional per-payment override, https-only), `expiration_minutes` (default 15, bounded min/max).
   - The merchant wallet is SERVER CONFIG (env), never a request parameter — the public API cannot direct payments to arbitrary wallets.
   - Generates a unique reference public key per payment, locks the amount at creation, computes `expires_at`. Payment IDs are time-ordered (UUIDv7 or equivalent).
   - Returns: `payment_id`, `solana_pay_url`, `qr_code` (base64 PNG), `reference`, `status: "pending"`, `expires_at`, amount in decimal + base units.
2. **GET /api/v1/payments/{id}** — status: `status`, `tx_signature` (when found), `confirmed_at`, `finalized_at`, `expires_at`, failure reason when failed.
3. **GET /healthz**, **GET /readyz** (checks DB + RPC), **GET /metrics** (Prometheus).
4. **POST /api/v1/payments/{id}/webhook/replay** — manually re-send a webhook.

## Payment Lifecycle (explicit state machine)

`pending → processing (matching tx found, awaiting confirmation) → confirmed → finalized`

Terminal alternatives: `expired` (reaper marks payments past `expires_at`), `failed` (tx found but validation failed — wrong amount/mint/recipient; reason recorded).

- Watcher is idempotent: re-processing the same transaction never changes state or re-fires webhooks. Enforce with a unique constraint on `(payment_id, tx_signature)` and a webhook-sent flag guarded by a row lock.
- Expired payments stop being watched. If a valid tx is detected for an expired payment, record it (`detected_tx` field) for manual reconciliation — never auto-confirm late payments.

## Confirmation Semantics

- Solana has three commitment levels: processed / confirmed / finalized. Default policy: mark `confirmed` and fire the webhook at `confirmed` (~2s), then escalate to `finalized` (~13s) and emit a status-upgrade event. Policy is deployment-configurable.
- Webhook event types: `payment.confirmed`, `payment.finalized` (optionally `payment.expired`).

## Watcher / Verification Service

- Background worker: bounded worker pool + per-RPC rate limiter; graceful shutdown via context; all outbound calls have timeouts.
- Detection: `getSignaturesForAddress(reference)` per pending reference (standard Solana Pay pattern — references appear as account keys), then `getTransaction` with `jsonParsed` encoding. Verify early that solana-go's jsonParsed handling is usable.
- Validation on match — strict:
  - Recipient is exactly the configured merchant wallet.
  - Correct mint (USDC) via pre/post token balances; do NOT hand-decode SPL instruction data.
  - For SOL: lamport balance change of the recipient.
  - Amount exactly matches the expected base units (mismatch → `failed` with reason).
  - Reference key present in the transaction's account keys.
- Claim work from Postgres with `SELECT ... FOR UPDATE SKIP LOCKED` so multiple replicas are safe.
- Escalation pass: once confirmed, keep watching until finalized commitment, then set `finalized`.
- Scale note: per-reference polling is O(pending payments) — fine to start. Put the poll strategy behind an interface so a wallet-scanning or websocket-based strategy can replace it without touching validation logic.

## Webhooks

- HMAC-SHA256 over `timestamp + "." + raw_body`. Headers: `X-Signature-256`, `X-Webhook-Timestamp`. Reject timestamps skewed more than 5 minutes (replay protection). Compare signatures in constant time. Secret from env, rotatable.
- Delivery: at-least-once semantics (consumers must tolerate duplicates). POST via an explicit-timeout `http.Client` (e.g. 10s — Go's default has none). Retries with exponential backoff + jitter, max attempts (e.g. 8), then dead-letter state surfaced in the API.

## Tech Stack

- Go 1.23+
- Router: chi
- Solana: gagliardetto/solana-go
- DB: PostgreSQL + jackc/pgx v5 (pgxpool); migrations: goose (sqlc optional for the store layer)
- QR: skip2/go-qrcode
- Money parsing: shopspring/decimal (edge only)
- Config: caarlos0/env (+ godotenv in dev only) — no Viper
- Logging: slog, structured JSON, `payment_id`/`reference` on every log line
- Validation: go-playground/validator
- API docs: spec-first OpenAPI (api/openapi.yaml) with oapi-codegen — the spec is the source of truth, no swaggo comment drift
- Background: long-running goroutines + errgroup + context cancellation; River later if a real job queue is needed

## Project Structure

```
cmd/server/main.go
internal/
  config/
  api/          # chi handlers, middleware, error envelope
  payment/      # domain: creation, state machine, expiry reaper
  solanapay/    # PURE: URL building, reference generation, QR — no I/O, fully unit-tested
  chain/        # RPC client + tx fetch/validation — I/O, integration-tested
  watcher/
  webhook/
  store/        # pgx repositories
api/openapi.yaml
migrations/
examples/       # curl + JS integration snippet
Dockerfile
docker-compose.yml   # app + postgres
.env.example
```

## Correctness & Operations

- context.Context propagation everywhere; graceful shutdown drains watcher + webhook workers.
- Metrics: payments created/confirmed/failed/expired, webhook attempts/latency, RPC latency/errors.
- Unit tests: URL generation, amount parsing, validation logic, HMAC signing.
- Integration tests against a local `solana-test-validator` in CI (not just devnet).
- No goroutine leaks; every HTTP call has a timeout.

## Deliverables

1. Working core flow: Create → Solana Pay URL + QR → watcher validates on-chain → signed webhook.
2. README: setup, security model (with the precise compromise-safety claim), ops notes.
3. `.env.example`
4. `api/openapi.yaml` (source of truth, code-generated types)
5. curl examples + minimal JS frontend snippet
6. Dockerfile + docker-compose
7. Tests: unit + test-validator integration

## Build Order

1. Config + store + migrations + health endpoints
2. Payment creation (reference, URL, QR) — pure parts fully unit-tested
3. Watcher: detection + strict validation + state machine (verified against test-validator)
4. Webhooks: signing, retries, replay
5. Expiry reaper, metrics, docs polish

Prioritize correctness and security over features. Keep modules clean — multi-merchant support, dashboard, and optional fee collection come later.
