import { createHmac, timingSafeEqual } from 'node:crypto';

// Payment links: a signed URL that anyone can redeem for exactly one
// invoice with exactly these parameters. The signature locks the amount and
// currency, so a leaked link can't be turned into an invoice for a
// different price. Server-side only (LINK_SECRET never reaches the browser).

export type LinkParams = {
	amount: string;
	currency: string; // "SOL" or mint address
	label: string;
	message: string;
	exp_minutes: number;
	link_exp: number; // unix seconds; 0 = link never expires
};

export const canonical = (p: LinkParams): string =>
	[p.amount, p.currency, p.label, p.message, String(p.exp_minutes), String(p.link_exp)].join('|');

export function sign(p: LinkParams): string {
	return createHmac('sha256', process.env.LINK_SECRET ?? '').update(canonical(p)).digest('hex');
}

export function verify(p: LinkParams, sig: string): { ok: boolean; reason?: string } {
	if (!process.env.LINK_SECRET) return { ok: false, reason: 'LINK_SECRET not configured' };
	const want = Buffer.from(sign(p));
	const got = Buffer.from(sig ?? '');
	if (want.length !== got.length || !timingSafeEqual(want, got)) {
		return { ok: false, reason: 'invalid signature' };
	}
	if (p.link_exp > 0 && Math.floor(Date.now() / 1000) > p.link_exp) {
		return { ok: false, reason: 'link expired' };
	}
	return { ok: true };
}

export function linkURL(p: LinkParams, origin: string): string {
	const q = new URLSearchParams({
		amount: p.amount,
		currency: p.currency,
		label: p.label,
		message: p.message,
		exp: String(p.exp_minutes),
		until: String(p.link_exp),
		sig: sign(p)
	});
	return `${origin}/pay?${q.toString()}`;
}
