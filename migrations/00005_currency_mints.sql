-- +goose Up
-- Currency is "SOL" or a base58 mint address (app-validated).
ALTER TABLE payments DROP CONSTRAINT payments_currency_check;
ALTER TABLE payments ADD CONSTRAINT payments_currency_check CHECK (currency <> '');

ALTER TABLE payments ADD COLUMN decimals smallint NOT NULL DEFAULT 9;

-- +goose Down
ALTER TABLE payments DROP COLUMN IF EXISTS decimals;
ALTER TABLE payments DROP CONSTRAINT payments_currency_check;
ALTER TABLE payments ADD CONSTRAINT payments_currency_check CHECK (currency IN ('SOL','USDC'));
