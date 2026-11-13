import { json, error } from '@sveltejs/kit';
import type { RequestHandler } from './$types';
import { verify, type LinkParams } from '#lib/links';

// Public: redeem a signed payment link for one invoice. No API key in the
// browser — the gateway call happens server-side, and only link parameters
// pass through (the signature locks amount/currency).

const GATEWAY_URL = process.env.GATEWAY_URL ?? 'http://localhost:8080';
const GATEWAY_API_KEY = process.env.GATEWAY_API_KEY ?? '';

export const POST: RequestHandler = async ({ request }) => {
	const body = await request.json().catch(() => ({}));
	const params: LinkParams = {
		amount: String(body.amount ?? ''),
		currency: String(body.currency ?? 'SOL'),
		label: String(body.label ?? ''),
		message: String(body.message ?? ''),
		exp_minutes: Number(body.exp ?? 15),
		link_exp: Number(body.until ?? 0)
	};
	const check = verify(params, String(body.sig ?? ''));
	if (!check.ok) throw error(403, check.reason);

	const res = await fetch(`${GATEWAY_URL}/api/v1/payments`, {
		method: 'POST',
		headers: {
			Authorization: `Bearer ${GATEWAY_API_KEY}`,
			'Content-Type': 'application/json'
		},
		body: JSON.stringify({
			amount: params.amount,
			currency: params.currency === 'SOL' ? undefined : params.currency,
			order_id: `link-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`,
			label: params.label || undefined,
			message: params.message || undefined,
			expiration_minutes: params.exp_minutes
		})
	});
	const created = await res.json().catch(() => null);
	if (!res.ok) {
		throw error(res.status, created?.error?.message ?? 'gateway rejected the invoice');
	}
	return json(created);
};
