import { json } from '@sveltejs/kit';
import type { RequestHandler } from './$types';
import { linkURL, type LinkParams } from '#lib/links';

// Admin-side: sign a reusable payment link. Intended for the (local/private)
// admin UI; in production keep the admin UI behind auth/VPN — anyone with
// access can mint links for any amount.
export const POST: RequestHandler = async ({ request, url }) => {
	const body = await request.json().catch(() => ({}));
	const params: LinkParams = {
		amount: String(body.amount ?? ''),
		currency: String(body.currency ?? 'SOL'),
		label: String(body.label ?? ''),
		message: String(body.message ?? ''),
		exp_minutes: Number(body.exp ?? 15),
		link_exp: Number(body.until ?? 0)
	};
	const origin = body.origin ? String(body.origin) : url.origin;
	return json({ url: linkURL(params, origin) });
};
