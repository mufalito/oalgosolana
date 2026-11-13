import { json, error } from '@sveltejs/kit';
import type { RequestHandler } from './$types';

// Public: checkout details for resuming an in-progress payment (the page
// already holds the payment id in sessionStorage). Safe fields only.

const GATEWAY_URL = process.env.GATEWAY_URL ?? 'http://localhost:8080';
const GATEWAY_API_KEY = process.env.GATEWAY_API_KEY ?? '';

export const GET: RequestHandler = async ({ params }) => {
	const res = await fetch(`${GATEWAY_URL}/api/v1/payments/${params.id}`, {
		headers: { Authorization: `Bearer ${GATEWAY_API_KEY}` }
	});
	if (!res.ok) throw error(res.status, 'unknown payment');
	const p = await res.json();
	return json({
		payment_id: p.payment_id,
		amount: p.amount,
		currency: p.currency,
		qr_code: p.qr_code,
		deposit_address: p.deposit_address,
		expires_at: p.expires_at
	});
};
