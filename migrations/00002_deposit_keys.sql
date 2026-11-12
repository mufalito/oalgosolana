-- +goose Up
ALTER TABLE payments ADD COLUMN sweep_tx text NOT NULL DEFAULT '';

-- Deposit keypairs for custodial deposit-address mode (DEPOSIT_ADDRESS_MODE=true).
-- Secrets are AES-256-GCM encrypted with DEPOSIT_MASTER_KEY (env, never stored).
CREATE TABLE deposit_keys (
  payment_id       uuid PRIMARY KEY REFERENCES payments(id),
  public_key       text NOT NULL UNIQUE,
  encrypted_secret bytea NOT NULL,
  created_at       timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS deposit_keys;
ALTER TABLE payments DROP COLUMN IF EXISTS sweep_tx;
