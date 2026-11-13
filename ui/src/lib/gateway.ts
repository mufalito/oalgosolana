// Thin typed client for the gateway, via the server-side proxy.
export type PaymentCreated = {
	payment_id: string;
	order_id: string;
	amount: string;
	amount_base: string;
	currency: string; // "SOL" or mint address
	solana_pay_url: string;
	qr_code: string; // base64 PNG
	deposit_address: string;
	status: string;
	expires_at: string;
};

export type PaymentStatus = {
	payment_id: string;
	order_id: string;
	amount: string;
	amount_base: string;
	currency: string;
	status: 'pending' | 'processing' | 'confirmed' | 'finalized' | 'expired';
	deposit_address: string;
	tx_signature?: string;
	confirmed_at?: string;
	finalized_at?: string;
	expires_at: string;
};

export type CreateInput = {
	amount: string;
	currency?: string;
	order_id: string;
	label?: string;
	message?: string;
	webhook_url?: string;
	expiration_minutes?: number;
};

async function call<T>(path: string, init?: RequestInit): Promise<T> {
	const res = await fetch(`/ui-api/${path}`, {
		...init,
		headers: { 'Content-Type': 'application/json', ...(init?.headers ?? {}) }
	});
	if (!res.ok) {
		const body = await res.json().catch(() => ({ message: String(res.status) }));
		throw new Error(body.message ?? 'request failed');
	}
	return res.json();
}

export const gateway = {
	create: (input: CreateInput) =>
		call<PaymentCreated>('v1/payments', { method: 'POST', body: JSON.stringify(input) }),
	get: (id: string) => call<PaymentStatus>(`v1/payments/${id}`),
	list: () => call<PaymentStatus[]>('v1/payments'),
	replay: (id: string) => call<{ status: string }>(`v1/payments/${id}/webhook/replay`, { method: 'POST' })
};

export const STATUS_COLORS: Record<string, string> = {
	pending: 'text-amber-400',
	processing: 'text-sky-400',
	confirmed: 'text-emerald-400',
	finalized: 'text-emerald-300',
	expired: 'text-zinc-500'
};

export function currencyLabel(currency: string): string {
	return currency === 'SOL' ? 'SOL' : `${currency.slice(0, 4)}…${currency.slice(-4)}`;
}
