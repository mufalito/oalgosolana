# Create a payment (SOL)
curl -s -X POST http://localhost:8080/api/v1/payments \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
        "amount": "1.50",
        "currency": "SOL",
        "order_id": "order-2024-0001",
        "label": "Coffee Shop",
        "message": "Order #0001",
        "webhook_url": "https://merchant.example.com/webhooks/solanapay",
        "expiration_minutes": 15
      }'
# => {"payment_id":"...","solana_pay_url":"solana:...?amount=1.500000000&reference=...",
#     "qr_code":"iVBORw0KGgo...","status":"pending", ...}

# Create a payment (USDC)
curl -s -X POST http://localhost:8080/api/v1/payments \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"amount": "25.00", "currency": "USDC", "order_id": "order-2024-0002"}'

# Poll status
curl -s http://localhost:8080/api/v1/payments/$PAYMENT_ID \
  -H "Authorization: Bearer $API_KEY"
# => {"status":"confirmed","tx_signature":"...","confirmed_at":"...",
#     "webhook":{"payment.confirmed":"sent"}, ...}

# Replay a dead-lettered webhook
curl -s -X POST http://localhost:8080/api/v1/payments/$PAYMENT_ID/webhook/replay \
  -H "Authorization: Bearer $API_KEY"
