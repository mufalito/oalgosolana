#!/bin/sh
# Examples for the vault-based payment gateway.
# Set: API_BASE and API_KEY, e.g.
#   API_BASE=http://localhost:8080 API_KEY=your-key ./examples/create-payment.sh

: "${API_BASE:=http://localhost:8080}"
: "${API_KEY:?set API_KEY}"
AUTH="Authorization: Bearer $API_KEY"

# --- 1. Create a SOL invoice ---------------------------------------------
RESP=$(curl -s -X POST "$API_BASE/api/v1/payments" -H "$AUTH" -H 'Content-Type: application/json' -d '{
  "amount": "1.50",
  "order_id": "order-0001",
  "label": "Coffee Shop",
  "message": "Order #0001",
  "webhook_url": "https://merchant.example.com/webhooks/solanapay",
  "expiration_minutes": 15
}')
echo "$RESP" | python3 -m json.tool

PAYMENT_ID=$(echo "$RESP" | python3 -c 'import json,sys;print(json.load(sys.stdin)["payment_id"])')
DEPOSIT=$(echo "$RESP" | python3 -c 'import json,sys;print(json.load(sys.stdin)["deposit_address"])')
echo
echo "Customer pays exactly this, from ANY wallet or exchange:"
echo "  address: $DEPOSIT"
echo "  amount:  1.50 SOL"
echo "The qr_code field is a base64 PNG of the solana_pay_url."

# --- 2. Create an SPL token invoice --------------------------------------
# The mint must be allowlisted on the gateway (ACCEPTED_MINTS env).
# Example mint: USDC on mainnet.
MINT="EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"
RESP=$(curl -s -X POST "$API_BASE/api/v1/payments" -H "$AUTH" -H 'Content-Type: application/json' -d "{
  \"amount\": \"25.00\",
  \"currency\": \"$MINT\",
  \"order_id\": \"order-0002\"
}")
echo "$RESP" | python3 -m json.tool
echo
echo "Customer sends 25.00 USDC to the deposit_address (same UX as SOL:"
echo "their wallet creates the vault's token account as part of the send)."

# --- 3. Poll status -------------------------------------------------------
# pending -> processing (partial funds) -> confirmed (sweep sent)
# -> finalized (sweep landed; money is in the merchant wallet)
curl -s "$API_BASE/api/v1/payments/$PAYMENT_ID" -H "$AUTH" | python3 -m json.tool

# --- 4. Verify an address offline (catches misconfiguration) --------------
# Recompute any deposit address without RPC and compare with what the API gave you.
# go run ./cmd/derive -merchant "$MERCHANT_WALLET" -program "$DEPOSIT_PROGRAM_ID" \
#   -invoice "$PAYMENT_ID" [-mint "$MINT"]

# --- 5. Replay a dead-lettered webhook ------------------------------------
curl -s -X POST "$API_BASE/api/v1/payments/$PAYMENT_ID/webhook/replay" -H "$AUTH"
echo
