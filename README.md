# oalgosolana — Solana Pay Gateway

Self-hosted Solana payment gateway built around an **on-chain vault program**:
every invoice gets a unique derived deposit address payable from any wallet
or exchange — plain address + amount, no Solana Pay support, no reference,
no memo — and funds can only ever leave a vault to the merchant's wallet,
enforced on-chain.

**Status: prototype.** Core flow is live-tested on Solana devnet; the vault
program is unaudited. Read [vault-program/README.md](vault-program/README.md)
before putting real money behind it.

## How it works

1. `POST /api/v1/payments` → the deposit address is the vault PDA derived
   from `["vault", merchant, invoice_id]`. **Nothing is created on-chain**:
   no transaction, no keys, no rent — unpaid invoices cost zero.
2. The customer pays the address from any wallet or exchange. Their own
   transfer creates the vault (and pays its rent).
3. The watcher sees the balance reach the invoiced amount and sends the
   `sweep` instruction (fee paid by the gateway hot wallet). The program
   moves the **entire balance** to the merchant embedded in the seeds and
   the account closes — a sweep naming any other destination derives a
   different address and is rejected on-chain (`ConstraintSeeds`).
4. The confirmed sweep is the receipt: `payment.confirmed` webhook, then
   `payment.finalized` when the sweep finalizes.

The destination is baked into the address derivation, not passed as an
argument — the gateway's hot wallet can pay fees and *ask* for sweeps, but
cannot redirect or steal funds.

## Security model

- A full server compromise can: hand customers different addresses for NEW
  invoices (detectable — the merchant can recompute every valid address
  offline), delay sweeps (DoS), and burn hot-wallet fees. It cannot redirect
  or steal money already paid into any vault.
- The one real kill switch is the **program upgrade authority**: compromise
  it and all vaults can be drained. Revoke it after deployment
  (`solana program set-upgrade-authority --final`) or multisig it.
- No audit. Prototype code.

## API

Spec: [`api/openapi.yaml`](api/openapi.yaml) (source of truth). All
endpoints except `/healthz`, `/readyz`, `/metrics` require
`Authorization: Bearer <key>` (constant-time compared, per-key token-bucket
rate limiting). Errors are `{"error":{"code":"...","message":"..."}}`.

| Endpoint | Purpose |
|---|---|
| `POST /api/v1/payments` | Create invoice (SOL; amount as decimal string) |
| `GET /api/v1/payments/{id}` | Status incl. sweep/payment signatures |
| `POST /api/v1/payments/{id}/webhook/replay` | Requeue dead/pending deliveries |
| `GET /healthz` `/readyz` `/metrics` | Liveness, readiness (DB+RPC), Prometheus |

Amounts: decimal strings at the API edge only; stored and compared as
`uint64` lamports. Minimum invoice = the rent-exempt minimum for an empty
account, read live from RPC (the customer's transfer must be able to create
the vault).

## Payment lifecycle

```
pending → processing → confirmed → finalized
   │          │             └─ sweep lands on-chain (receipt)
   │          └─ partial funds seen, amount not yet reached
   └─ expires_at passed → expired
```

- **Underpayment** accumulates: the invoice stays `processing` until the
  vault holds the full amount; more transfers add up.
- **Overpayment / rent**: the sweep takes everything; the merchant receives
  the excess and the account rent. Reconcile manually.
- **Late payment** (after expiry): the reaper detects vault balance > 0 and
  sweeps anyway — money in a vault always reaches the merchant.
- **Failed/dropped sweep**: re-sent on the next tick while the vault holds
  funds; `finalized` only after a sweep has actually landed (`err == nil`,
  finalized commitment).
- Anyone can call `sweep` — safe by construction; lets the merchant run an
  independent sweeper as a checkpoint on the gateway.

## Webhooks

Events: `payment.confirmed`, `payment.finalized`, `payment.expired` (only
when a `webhook_url` is set — per payment, https-only).

Headers: `X-Signature-256: sha256=<hex>` = HMAC-SHA256 over
`X-Webhook-Timestamp + "." + raw_body` with `WEBHOOK_SECRET`; verify with a
constant-time comparison and reject skew > 5 min (see
`examples/frontend.js` for a Node receiver). At-least-once delivery,
exponential backoff with jitter capped at 10 min, dead-letter after
`WEBHOOK_MAX_ATTEMPTS`, requeueable via the replay endpoint.

## Configuration

All configuration is environment variables; see [.env.example](.env.example).
Required: `API_KEYS`, `MERCHANT_WALLET` (the cold wallet), `RPC_URL`
(own node or paid provider — never public mainnet RPC), `WEBHOOK_SECRET`
(≥32 bytes), `DATABASE_URL`, `DEPOSIT_PROGRAM_ID`, `GATEWAY_HOT_WALLET`
(sweep fees only).

## Operations

- Graceful shutdown: SIGTERM → HTTP drain (30 s) → watcher/webhook/reaper
  cancel → pool close.
- Metrics (`/metrics`): `payments_created_total{currency}`,
  `payments_confirmed_total`, `payments_expired_total`,
  `webhook_attempts_total{result}`, `webhook_delivery_seconds`,
  `rpc_request_seconds{method}`, `rpc_errors_total{method}`.
- Readiness (`/readyz`): `SELECT 1` + RPC `getHealth`, 2 s budget each.
- Watcher runs one instance per database (poll is idempotent but not
  row-locked across RPC calls). Webhook delivery claims are
  `FOR UPDATE SKIP LOCKED` (replica-safe).

## Repository layout

```
cmd/server/        HTTP server entrypoint
internal/
  api/             chi handlers, middleware, error envelope
  apierr/          machine-readable error codes (leaf package)
  payment/         payment domain service (creation, lookup)
  solanapay/       PURE: amounts, URLs, QR — no I/O
  chain/           RPC client (balances, signatures, statuses, rent)
  watcher/         state machine (balance trigger → sweep → finality)
  webhook/         HMAC-signed deliveries, retries, dead-letter
  reaper/          expiry + late-fund sweeping
  vault/           client for the on-chain vault program
  store/           pgx repositories + embedded goose migrations
  config/ metrics/
api/openapi.yaml   API spec (source of truth)
migrations/        SQL migrations (embedded in the binary)
vault-program/     Anchor vault program (derived vaults, sweep-only)
examples/          curl + JS integration snippets
```

## Development

```sh
go build ./... && go vet ./... && go test ./...
```

Unit tests (amount parsing, URL/QR, state machine, HMAC, backoff) run
everywhere; watcher/webhook tests need a local Postgres (skipped when
unreachable, isolated per-package databases).

## Out of scope (by design)

Multi-merchant support, dashboards, fee collection, refunds/withdrawals
(merchants sign payouts from their own wallet), USDC/tokens (needs
pre-created token accounts; revisit with an audited program).
