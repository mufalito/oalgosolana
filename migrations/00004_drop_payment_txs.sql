-- +goose Up
-- Vault mode uses balance-based detection; candidate-signature claims are gone.
DROP TABLE IF EXISTS payment_txs;

-- +goose Down
CREATE TABLE payment_txs (
  payment_id  uuid NOT NULL REFERENCES payments(id),
  tx_signature text NOT NULL,
  PRIMARY KEY (payment_id, tx_signature)
);
