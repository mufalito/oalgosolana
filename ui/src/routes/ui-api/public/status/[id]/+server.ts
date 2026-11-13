import { json, error } from '@sveltejs/kit';
import type { RequestHandler } from './$types';

// Public: checkout status for a payment id. Only safe fields are returned —
// no order metadata, no webhook state, no merchant info.

const GATEWAY_URL = process.env.GATEWAY_URL ?? 'http://localhost:8080';
const GATEWAY_API_KEY = process.env.GATEWAY_API_KEY ?? '';

export const GET: RequestHandler = async ({ params }) => {
	const res = await fetch(`${GATEWAY_URL}/api/v1/payments/${params.id}`, {
		headers: { Authorization: `Bearer ${GATEWAY_API_KEY}` }
	});
	if (!res.ok) throw error(res.status, 'unknown payment');
	const p = await res.json();
	return json({
		status: p.status,
		amount: p.amount,
		currency: p.currency,
		deposit_address: p.deposit_address,
		tx_signature: p.tx_signature ?? null,
		confirmed_at: p.confirmed_at ?? null,
		finalized_at: p.finalized_at ?? null,
		expires_at: p.expires_at
	});
};
