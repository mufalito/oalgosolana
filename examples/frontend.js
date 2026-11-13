// Minimal integration snippets: create payments (SOL or SPL), render the QR,
// poll status, and verify webhook signatures server-side.
//
// Browser usage requires a small trusted backend proxy holding the API key;
// never expose API keys to browsers.

const API = "http://localhost:8080";
const API_KEY = process.env.SOLANAPAY_API_KEY;

// 1. Create a payment (server-side). Currency is "SOL" or a base58 mint
//    address from the gateway's ACCEPTED_MINTS allowlist.
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

// SOL invoice
const sol = await createPayment({
  amount: "1.50",
  order_id: "order-0001",
  label: "Coffee Shop",
  message: "Order #0001",
});

// SPL invoice (e.g. USDC mainnet mint)
const usdc = "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v";
const spl = await createPayment({ amount: "25.00", currency: usdc, order_id: "order-0002" });

// 2. Show the customer what to pay. Two options:
//    a) the raw deposit address + amount (works in ANY wallet or exchange —
//       for tokens the wallet creates the vault's token account itself)
//    b) the QR (base64 PNG of the solana_pay_url, which carries spl-token
//       for capable wallets)
function render(payment) {
  const row = document.createElement("div");
  row.innerHTML = `
    <p>Send <b>${payment.amount} ${payment.currency === "SOL" ? "SOL" : "tokens"}</b> to
       <code>${payment.deposit_address}</code></p>`;
  const img = document.createElement("img");
  img.src = `data:image/png;base64,${payment.qr_code}`;
  img.alt = "Scan with any Solana wallet";
  row.appendChild(img);
  document.body.appendChild(row);
}

// 3. Poll status. Lifecycle:
//    pending -> processing (partial funds seen) -> confirmed (sweep sent)
//    -> finalized (sweep landed on-chain, money is in the merchant wallet)
async function pollStatus(paymentId, onUpdate, intervalMs = 3000) {
  return new Promise((resolve) => {
    const timer = setInterval(async () => {
      const res = await fetch(`${API}/api/v1/payments/${paymentId}`, {
        headers: { Authorization: `Bearer ${API_KEY}` },
      });
      const payment = await res.json();
      onUpdate(payment);
      if (["finalized", "expired"].includes(payment.status)) {
        clearInterval(timer);
        resolve(payment);
      }
    }, intervalMs);
  });
}

// Note: "confirmed" means the vault was funded and the sweep transaction was
// sent; "finalized" means the sweep landed — treat finalized as "money in
// the bank" for fulfillment if you want maximum safety, or confirmed for
// ~faster release.

// 4. (Optional) Verify addresses offline: vault addresses are deterministic
//    PDA(["vault", merchant, program, invoice_id]). If you run your own
//    build of cmd/derive you can recompute and compare every address the
//    gateway hands out.

// 5. Webhook receiver (Node/Express): verify X-Signature-256.
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
