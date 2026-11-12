-- +goose Up
CREATE TABLE payments (
  id            uuid PRIMARY KEY,                -- UUIDv7
  order_id      text NOT NULL,
  amount_base   bigint NOT NULL CHECK (amount_base > 0),
  currency      text NOT NULL CHECK (currency IN ('SOL','USDC')),
  status        text NOT NULL CHECK (status IN ('pending','processing','confirmed','finalized','expired','failed')),
  reference     text NOT NULL UNIQUE,            -- base58 pubkey
  merchant_wallet text NOT NULL,
  label         text NOT NULL DEFAULT '',
  message       text NOT NULL DEFAULT '',
  webhook_url   text NOT NULL DEFAULT '',        -- per-payment override, '' = none
  expires_at    timestamptz NOT NULL,
  created_at    timestamptz NOT NULL DEFAULT now(),
  tx_signature  text NOT NULL DEFAULT '',
  detected_tx   text NOT NULL DEFAULT '',        -- late tx after expiry
  confirmed_at  timestamptz,
  finalized_at  timestamptz,
  failure_reason text NOT NULL DEFAULT '',
  UNIQUE (order_id)                              -- single-merchant: order_id globally unique
);
CREATE INDEX idx_payments_status_expires ON payments (status, expires_at);
CREATE INDEX idx_payments_status_ref ON payments (status, reference);

CREATE TABLE payment_txs (                        -- idempotency guard
  payment_id  uuid NOT NULL REFERENCES payments(id),
  tx_signature text NOT NULL,
  PRIMARY KEY (payment_id, tx_signature)
);

CREATE TABLE webhook_deliveries (
  id          bigserial PRIMARY KEY,
  payment_id  uuid NOT NULL REFERENCES payments(id),
  event_type  text NOT NULL,                     -- payment.confirmed | payment.finalized | payment.expired
  payload     jsonb NOT NULL,
  status      text NOT NULL CHECK (status IN ('pending','sent','dead')),
  attempts    int NOT NULL DEFAULT 0,
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  last_error  text NOT NULL DEFAULT '',
  sent_at     timestamptz,
  UNIQUE (payment_id, event_type)                -- at-least-once per event, never duplicate enqueue
);

-- +goose Down
DROP TABLE IF EXISTS webhook_deliveries;
DROP TABLE IF EXISTS payment_txs;
DROP TABLE IF EXISTS payments;
