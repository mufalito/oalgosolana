-- +goose Up
-- Deposit vault program v2 uses derived addresses: no per-invoice secrets.
DROP TABLE IF EXISTS deposit_keys;

-- +goose Down
CREATE TABLE deposit_keys (
  payment_id       uuid PRIMARY KEY REFERENCES payments(id),
  public_key       text NOT NULL UNIQUE,
  encrypted_secret bytea NOT NULL,
  created_at       timestamptz NOT NULL DEFAULT now()
);
