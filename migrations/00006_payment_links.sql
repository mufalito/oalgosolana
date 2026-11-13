-- +goose Up
-- Server-side payment links: short opaque ids (like buy.example.com/c/pl_xxx).
-- Parameters live in the database, not in the URL.
CREATE TABLE payment_links (
  id             text PRIMARY KEY,            -- pl_<20 base62 chars>
  amount_base    bigint NOT NULL CHECK (amount_base > 0),
  decimals       smallint NOT NULL DEFAULT 9,
  currency       text NOT NULL,               -- "SOL" or mint address
  label          text NOT NULL DEFAULT '',
  message        text NOT NULL DEFAULT '',
  expiration_minutes int NOT NULL DEFAULT 15,
  link_expires_at   timestamptz,              -- NULL = link never expires
  created_at     timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS payment_links;
