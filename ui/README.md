# oalgosolana UI

Admin/invoice UI for the payment gateway. SvelteKit (Svelte 5, TypeScript,
Tailwind). Talks to the gateway **only through a server-side proxy**, so the
API key never reaches the browser.

## Run

```sh
cp .env.example .env   # set GATEWAY_URL + GATEWAY_API_KEY
npm install
npm run dev            # http://localhost:5173
```

Production: `npm run build` (adapter-node) and run the node server with the
same env vars.

## What it does

- **New invoice** (`/`): amount, SOL or allowlisted SPL mint, order id,
  label/message, webhook URL, expiration. Shows the deposit address + QR and
  live status (`pending → processing → confirmed → finalized`).
- **Payment links**: signed URLs (`/pay?amount=…&sig=…`, HMAC with
  `LINK_SECRET`) that redeem into a hosted checkout — amount and currency
  are locked by the signature, each open creates one invoice, and a reload
  resumes the same invoice (sessionStorage). Share anywhere.
- **Hosted checkout** (`/pay`): public page with address + QR + countdown +
  live status; no API key in the browser (server-side redeem/status
  endpoints return only safe fields).
- **Payments** (`/invoices`): the 50 most recent invoices with status,
  auto-refresh every 5s, webhook replay button.

⚠️ The admin UI (`/` and `/invoices`) has no authentication — anyone who can
reach it can mint invoices/links. Keep it on localhost or behind
auth/VPN in production. The hosted checkout (`/pay`) is safe to expose.

## Layout

```
src/routes/+page.svelte            invoice creation + payment display
src/routes/invoices/+page.svelte   payments list
src/routes/ui-api/[...path]/       server-side proxy to the gateway
src/lib/gateway.ts                 typed client (via the proxy)
```

The proxy adds the `Authorization` header server-side; the browser never
sees the API key.
