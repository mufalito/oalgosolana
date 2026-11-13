import { json, error } from '@sveltejs/kit';
import type { RequestHandler } from './$types';

// Server-side proxy: forwards UI requests to the gateway so the API key
// never reaches the browser. Configure in ui/.env:
//   GATEWAY_URL=http://localhost:8080
//   GATEWAY_API_KEY=...
const GATEWAY_URL = process.env.GATEWAY_URL ?? 'http://localhost:8080';
const GATEWAY_API_KEY = process.env.GATEWAY_API_KEY ?? '';

export const GET: RequestHandler = async ({ request, params, url }) => {
	const res = await fetch(`${GATEWAY_URL}/api/${params.path}${url.search}`, {
		headers: { Authorization: `Bearer ${GATEWAY_API_KEY}` }
	});
	return relay(res);
};

export const POST: RequestHandler = async ({ request, params, url }) => {
	const res = await fetch(`${GATEWAY_URL}/api/${params.path}${url.search}`, {
		method: 'POST',
		headers: {
			Authorization: `Bearer ${GATEWAY_API_KEY}`,
			'Content-Type': 'application/json'
		},
		body: await request.text()
	});
	return relay(res);
};

async function relay(res: Response) {
	const text = await res.text();
	if (!res.ok) {
		let message = text;
		try {
			message = JSON.parse(text)?.error?.message ?? text;
		} catch {
			/* keep raw */
		}
		throw error(res.status, message);
	}
	return json(text ? JSON.parse(text) : null, { status: res.status });
}
