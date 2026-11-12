// Minimal browser/Node snippet: create a payment, render the QR, poll status,
// and (server-side) verify webhook signatures.
//
// Browser usage requires a small trusted backend proxy that holds the API key;
// never expose API keys to browsers.

const API = "http://localhost:8080";
const API_KEY = process.env.SOLANAPAY_API_KEY;

// 1. Create a payment (server-side)
async function createPayment(order) {
  const res = await fetch(`${API}/api/v1/payments`, {
    method: "POST",
    headers: {
      Authorization: `Bearer ${API_KEY}`,
      "Content-Type": "application/json",
    },
    body: JSON.stringify(order),
  });
  if (!res.ok) throw new Error(`${res.status}: ${await res.text()}`);
  return res.json();
}

// 2. Render the QR (the `qr_code` field is a base64 PNG of the solana: URL)
function renderQR(payment) {
  const img = document.createElement("img");
  img.src = `data:image/png;base64,${payment.qr_code}`;
  img.alt = "Scan with any Solana Pay wallet";
  document.body.appendChild(img);
  // Wallets can also open payment.solana_pay_url directly (deep link).
  return payment.solana_pay_url;
}

// 3. Poll status
async function pollStatus(paymentId, onUpdate, intervalMs = 3000) {
  return new Promise((resolve) => {
    const timer = setInterval(async () => {
      const res = await fetch(`${API}/api/v1/payments/${paymentId}`, {
        headers: { Authorization: `Bearer ${API_KEY}` },
      });
      const payment = await res.json();
      onUpdate(payment);
      if (["confirmed", "finalized", "expired", "failed"].includes(payment.status)) {
        clearInterval(timer);
        resolve(payment);
      }
    }, intervalMs);
  });
}

// 4. Webhook receiver (Node/Express): verify X-Signature-256.
//    The signature is HMAC-SHA256 over `${X-Webhook-Timestamp}.${rawBody}`
//    with the shared WEBHOOK_SECRET. Reject skew > 5 minutes.
import crypto from "node:crypto";

export function verifySolanaPayWebhook(req, rawBody, secret, maxSkewMs = 5 * 60_000) {
  const ts = Number(req.get("X-Webhook-Timestamp"));
  if (!Number.isFinite(ts)) return false;
  if (Math.abs(Date.now() / 1000 - ts) > maxSkewMs / 1000) return false;
  const expected =
    "sha256=" +
    crypto
      .createHmac("sha256", secret)
      .update(`${ts}.${rawBody}`)
      .digest("hex");
  const got = req.get("X-Signature-256") ?? "";
  const a = Buffer.from(expected);
  const b = Buffer.from(got);
  return a.length === b.length && crypto.timingSafeEqual(a, b);
}
