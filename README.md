# oalgosolana — Solana Pay Gateway

Self-hosted Solana payment gateway with three payment modes:

| Mode | Customer needs | Custody |
|---|---|---|
| Reference (default) | Solana Pay-capable wallet (`solana:` link / QR) | none — funds go straight to your wallet |
| Deposit vault | any wallet/exchange, plain address + amount | constrained — funds can only leave to your cold wallet, enforced on-chain |

**Status: prototype.** Core flows are live-tested on Solana devnet (see
[Verification](#operations)); the vault program is unaudited. Read
[vault-program/README.md](vault-program/README.md) before putting real money
behind it.

## Repository layout

```
cmd/server/        HTTP server entrypoint
internal/
  api/             chi handlers, middleware, error envelope
  apierr/          machine-readable error codes (leaf package)
  payment/         payment domain service (creation, lookup)
  solanapay/       PURE: amounts, URLs, references, QR — no I/O
  chain/           RPC detector + strict on-chain validation
  watcher/         state machine (pending→processing→confirmed→finalized)
  webhook/         HMAC-signed deliveries, retries, dead-letter
  reaper/          expiry + late-fund detection
  vault/           client for the on-chain vault program
  wallet/          AES-256-GCM secret encryption at rest
  store/           pgx repositories + embedded goose migrations
  config/ metrics/
api/openapi.yaml   API spec (source of truth)
migrations/        SQL migrations (embedded in the binary)
vault-program/     Anchor (otter-sec fork) deposit-vault program
examples/          curl + JS integration snippets
```
 Create payments, hand
customers a native Solana Pay URL + QR code, watch the chain, and receive
signed webhooks when payments confirm and finalize.

The gateway holds **zero private keys** and exposes **no fund-moving
endpoints**. Funds move directly from the customer's wallet to the merchant
wallet configured in server environment variables.

## Security model — the precise claim

> A full server compromise cannot redirect funds already received.
> Redirecting *future* payments would require changing the merchant wallet in
> server configuration, which is not settable through the public API and is
> outside the API's trust boundary (audit your env/config management).

Consequences:
- The API process never signs transactions and cannot move money, ever.
- Amounts are locked at creation; the watcher only *validates* on-chain
  transfers (recipient, mint, amount, reference) — it accepts a payment only
  if the on-chain transfer exactly matches.
- Webhooks are HMAC-SHA256 signed; receivers verify `X-Signature-256` over
  `timestamp + "." + raw_body` and reject timestamps skewed > 5 minutes.

## Quick start

```sh
cp .env.example .env        # then edit: API_KEYS, MERCHANT_WALLET, RPC_URL,
                            # WEBHOOK_SECRET, USDC_MINT_DEVNET (for devnet USDC)
docker compose up --build
curl -s localhost:8080/healthz   # {"status":"ok"}
```

Postgres runs in compose; migrations apply automatically on startup (embedded
goose). Create a payment (see `examples/create-payment.sh`):

```sh
curl -X POST localhost:8080/api/v1/payments \
  -H "Authorization: Bearer $API_KEY" -H 'Content-Type: application/json' \
  -d '{"amount":"1.50","currency":"SOL","order_id":"o1"}'
```

The response contains `solana_pay_url` (`solana:...`), a base64 PNG `qr_code`,
and the `reference` pubkey. Pay with any Solana Pay wallet, then poll
`GET /api/v1/payments/{id}` — the watcher moves it
`pending → processing → confirmed → finalized` and fires webhooks.

### Devnet smoke test

1. `CLUSTER=devnet`, `RPC_URL=https://api.devnet.solana.com` (or your own).
2. Set `MERCHANT_WALLET` to a wallet you control.
3. Create a payment, open the `solana:` URL in a devnet-enabled wallet
   (e.g. Phantom on devnet), send the exact amount.
4. Watch `GET /api/v1/payments/{id}` flip to `confirmed` (~seconds) and
   `finalized` (~15–30 s), or check `payments_confirmed_total` on `/metrics`.

## API

Full spec: [`api/openapi.yaml`](api/openapi.yaml) (source of truth).
All endpoints except `/healthz`, `/readyz`, `/metrics` require
`Authorization: Bearer <key>` (keys from `API_KEYS`, constant-time compared,
token-bucket rate limited per key). Errors are always
`{"error":{"code":"...","message":"..."}}`.

| Endpoint | Purpose |
|---|---|
| `POST /api/v1/payments` | Create payment (amount decimal string, SOL/USDC) |
| `GET /api/v1/payments/{id}` | Status incl. `tx_signature`, webhook statuses |
| `POST /api/v1/payments/{id}/webhook/replay` | Requeue dead/pending deliveries |
| `GET /healthz` `/readyz` `/metrics` | Liveness, readiness (DB+RPC), Prometheus |

Amounts: decimal strings at the API edge only (`"1.50"`); stored and compared
as `uint64` base units (SOL 9 decimals, USDC 6). More fractional digits than
the currency allows → `400 INVALID_AMOUNT`. Never `float64`.

## Payment lifecycle

```
pending → processing → confirmed → finalized
   │          │            └─ escalation pass at finalized commitment
   │          └─ validation failure → failed (WRONG_RECIPIENT | WRONG_MINT |
   │                                    WRONG_AMOUNT | MISSING_REFERENCE)
   └─ expires_at passed → expired (late valid tx recorded in detected_tx,
                                    never auto-confirmed)
```

- Idempotency: unique `(payment_id, tx_signature)`; re-processing the same tx
  never changes state or re-fires webhooks. Webhook queue has unique
  `(payment_id, event_type)` — at-least-once delivery, consumers must
  tolerate duplicates.
- Webhook delivery claims use `FOR UPDATE SKIP LOCKED` (replica-safe). The
  payment poll is idempotent by candidate-claim but NOT row-locked across
  its RPC calls: run one watcher per database.
- Confirmation policy (`CONFIRMATION_POLICY=confirmed|finalized`):
  `confirmed` fires the webhook at ~2 s and escalates to a `payment.finalized`
  event at finality; `finalized` fires once, only at finality.

## Webhooks

Events: `payment.confirmed`, `payment.finalized`, `payment.expired` (only when
a `webhook_url` is set — per payment, https-only).

Headers: `X-Signature-256: sha256=<hex>` = HMAC-SHA256 over
`X-Webhook-Timestamp + "." + raw_body` with `WEBHOOK_SECRET`;
verify with a constant-time comparison and reject skew > 5 min (see
`examples/frontend.js` for a Node receiver).

Retries: exponential backoff `2^attempt s × (0.5 + jitter)`, capped at 10 min,
up to `WEBHOOK_MAX_ATTEMPTS` (8), then dead-letter — surfaced in the GET
payment response (`"webhook":{"payment.confirmed":"dead"}`) and requeueable
via the replay endpoint.

## Configuration

All configuration is environment variables; see [.env.example](.env.example).
Notable:

- `MERCHANT_WALLET` — base58; funds destination; server config only.
- `RPC_URL` — required; use your own node or a paid provider. Never public
  mainnet RPC in production.
- `USDC_MINT_MAINNET` defaults to the Circle mainnet mint
  (`EPjFW...Dt1v`). `USDC_MINT_DEVNET` must be supplied by the operator
  (verify the current Circle devnet mint) — USDC payments on devnet without
  it are rejected with `INVALID_CURRENCY`.

## Operations

- Graceful shutdown: SIGTERM → HTTP drain (30 s) → watcher/webhook/reaper
  workers cancel → pool close.
- Metrics (`/metrics`): `payments_created_total{currency}`,
  `payments_confirmed_total`, `payments_failed_total{reason}`,
  `payments_expired_total`, `webhook_attempts_total{result}`,
  `webhook_delivery_seconds`, `rpc_request_seconds{method}`,
  `rpc_errors_total{method}`.
- Readiness (`/readyz`): `SELECT 1` + RPC `getHealth`, 2 s budget each.
- The poll strategy is O(pending references) behind the
  `chain.Detector` interface — swap in websockets/block-subscription later
  without touching validation logic.
- Watchdog limits: `POLL_INTERVAL`, `WORKER_COUNT`, per-RPC rate limiter.

## Development

```sh
go build ./... && go vet ./... && go test ./...
```

Unit tests (amount parsing, URL/QR, validation, state machine, HMAC, backoff)
run everywhere; watcher/webhook tests need a local Postgres (skipped when
unreachable). End-to-end tests against `solana-test-validator` run when
`SOLANA_TEST_VALIDATOR_URL` is set (or a validator is on localhost:8899).

## Optional deposit-address mode (vault program)

`DEPOSIT_ADDRESS_MODE=true` + `DEPOSIT_PROGRAM_ID` + `GATEWAY_HOT_WALLET`:
every invoice's deposit address is the vault PDA derived from
`["vault", merchant, invoice_id]` — **nothing is created on-chain at invoice
time** (zero cost for unpaid invoices; no create transaction that could
fail or be front-run). The customer's own transfer creates the vault and
pays its rent; the `sweep` instruction moves the **entire balance** to the
merchant embedded in the seeds, closing the account — a sweep naming any
other destination derives a different address and is rejected on-chain
(`ConstraintSeeds`, verified live on devnet). The gateway's hot wallet pays
sweep fees only; it cannot redirect funds. A full server compromise can
hand customers different addresses for NEW invoices (the merchant can
independently recompute every address to detect this) and delay sweeps, but
cannot redirect or steal money already paid in. Minimum invoice in this mode
is the rent-exempt minimum for an empty account, read live from RPC.
Guard the program's upgrade authority — revoke it
(`solana program set-upgrade-authority --final`) or multisig it.

Customers pay a plain address + amount from any wallet or exchange — no
Solana Pay support, no reference, no memo. Detection is balance-based: when
the vault holds the invoiced amount, the gateway sweeps and treats the
confirmed sweep as the receipt; failed or dropped sweeps are re-sent. Funds
arriving late (expired invoice) are detected by balance and swept too —
money in a vault always reaches the merchant. SOL only; USDC remains
reference-mode only. In this mode the non-custodial compromise-safety claim
applies to reference mode only — deposit mode is custody-constrained
(destination fixed on-chain), not key-free.

Per-invoice matching semantics in deposit mode: attribution is by unique
address; payments are confirmed once received lamports ≥ invoice amount
(overpayments and multiple partial transfers accumulate). `reference` in API
responses equals the deposit address.

## Out of scope (by design)

Reference-mode only: multi-merchant support, dashboards, fee collection. Refunds/withdrawals (merchants sign payouts from their own wallet),
multi-merchant support, dashboards, fee collection.
