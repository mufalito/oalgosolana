<script lang="ts">
	// Public hosted checkout: redeem a signed payment link (or arrive with
	// plain params in dev), show address + QR + countdown, poll status.
	import { onMount } from 'svelte';

	type Checkout = {
		payment_id: string;
		amount: string;
		currency: string;
		qr_code: string;
		deposit_address: string;
		solana_pay_url: string;
		expires_at: string;
	};

	type PublicStatus = {
		status: string;
		amount: string;
		currency: string;
		deposit_address: string;
		tx_signature: string | null;
		confirmed_at: string | null;
		finalized_at: string | null;
		expires_at: string;
	};

	let checkout = $state<Checkout | null>(null);
	let status = $state<PublicStatus | null>(null);
	let failure = $state('');
	let copied = $state(false);
	let secondsLeft = $state<number | null>(null);
	let origin = $state('');

	const STATUS_TEXT: Record<string, string> = {
		pending: 'Waiting for payment',
		processing: 'Partial payment seen — send the rest',
		confirmed: 'Payment confirmed — sweeping',
		finalized: 'Paid ✓',
		expired: 'Expired'
	};

	onMount(() => {
		origin = window.location.origin;
		const q = new URLSearchParams(window.location.search);
		const sig = q.get('sig') ?? '';
		const storageKey = `paylink:${sig}`;

		const resume = async (id: string) => {
			// refresh mid-checkout: resume the SAME invoice, never mint a new one
			try {
				const res = await fetch(`/ui-api/public/status/${id}`);
				if (res.ok) {
					const s = await res.json();
					if (s.status !== 'expired') {
						const r = await fetch(`/ui-api/public/checkout/${id}`);
						if (r.ok) {
							checkout = await r.json();
							status = s;
							poll(id);
							return true;
						}
					}
				}
			} catch {}
			sessionStorage.removeItem(storageKey);
			return false;
		};

		const redeem = async (body: Record<string, string>) => {
			const res = await fetch('/ui-api/public/redeem', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify(body)
			});
			if (!res.ok) throw new Error((await res.json().catch(() => ({}))).message ?? 'link rejected');
			return res.json();
		};

		(async () => {
			const cached = sessionStorage.getItem(storageKey);
			if (cached && (await resume(cached))) return;

			const body = {
				amount: q.get('amount') ?? '',
				currency: q.get('currency') ?? 'SOL',
				label: q.get('label') ?? '',
				message: q.get('message') ?? '',
				exp: q.get('exp') ?? '15',
				until: q.get('until') ?? '0',
				sig
			};
			if (!body.amount) {
				failure = 'Missing amount. Use a payment link created from the dashboard.';
				return;
			}
			try {
				const c: Checkout = await redeem(body);
				sessionStorage.setItem(storageKey, c.payment_id);
				checkout = c;
				poll(c.payment_id);
			} catch (e) {
				failure = e instanceof Error ? e.message : String(e);
			}
		})();
	});

	function poll(id: string) {
		setInterval(async () => {
			try {
				const res = await fetch(`/ui-api/public/status/${id}`);
				if (!res.ok) return;
				const next: PublicStatus = await res.json();
				status = next;
				if (next.status === 'finalized' || next.status === 'expired') return;
			} catch {}
		}, 3000);
	}

	$effect(() => {
		if (!checkout) return;
		const t = setInterval(() => {
			secondsLeft = Math.max(0, Math.floor((new Date(checkout!.expires_at).getTime() - Date.now()) / 1000));
		}, 1000);
		return () => clearInterval(t);
	});

	async function copyAddress() {
		await navigator.clipboard.writeText(checkout!.deposit_address);
		copied = true;
		setTimeout(() => (copied = false), 1200);
	}

	const fmtCountdown = (s: number) => `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`;
	const done = $derived(status?.status === 'finalized');
</script>

<svelte:head><title>Pay {checkout ? `${checkout.amount} ${checkout.currency === 'SOL' ? 'SOL' : 'tokens'}` : 'Checkout'}</title></svelte:head>

<div class="flex min-h-screen items-center justify-center bg-zinc-950 px-4 text-zinc-100">
	<div class="w-full max-w-md">
		{#if failure}
			<div class="rounded-xl border border-red-900 bg-red-950/40 p-8 text-center">
				<p class="text-lg font-medium text-red-300">Cannot open checkout</p>
				<p class="mt-2 text-sm text-red-400/80">{failure}</p>
			</div>
		{:else if checkout}
			<div class="rounded-xl border border-zinc-800 bg-zinc-900/50 p-8">
				{#if status?.status === 'expired'}
					<p class="text-center text-lg font-medium text-zinc-400">This invoice expired</p>
				{:else if done}
					<div class="py-6 text-center">
						<p class="text-3xl">✓</p>
						<p class="mt-2 text-xl font-semibold text-emerald-400">Payment received</p>
						<p class="mt-1 text-sm text-zinc-400">{checkout.amount} {checkout.currency === 'SOL' ? 'SOL' : 'tokens'} settled</p>
					</div>
				{:else}
					<p class="text-center text-sm text-zinc-400">Send exactly</p>
					<p class="mt-1 text-center text-3xl font-semibold">
						{status?.amount ?? checkout.amount}
						<span class="text-zinc-500">{checkout.currency === 'SOL' ? 'SOL' : 'tokens'}</span>
					</p>
					<div class="mt-6 flex justify-center">
						<img src="data:image/png;base64,{checkout.qr_code}" alt="Scan to pay" class="h-60 w-60 rounded-lg bg-white p-2" />
					</div>
					<p class="mt-4 text-center text-xs text-zinc-500">scan, or copy the address below</p>
					<div class="mt-3 flex items-center justify-center gap-2">
						<code class="max-w-xs truncate rounded-md border border-zinc-700 bg-zinc-900 px-3 py-1.5 text-xs">{checkout.deposit_address}</code>
						<button onclick={copyAddress} class="rounded-md border border-zinc-700 px-2 py-1.5 text-xs hover:bg-zinc-800">
							{copied ? '✓' : 'copy'}
						</button>
					</div>
					<p class="mt-6 text-center text-sm">
						<span class="font-medium {status?.status === 'processing' ? 'text-sky-400' : status?.status === 'confirmed' ? 'text-emerald-400' : 'text-zinc-300'}">
							{STATUS_TEXT[status?.status ?? 'pending'] ?? status?.status}
						</span>
					</p>
					{#if secondsLeft !== null && !done}
						<p class="mt-1 text-center text-xs text-zinc-500">expires in {fmtCountdown(secondsLeft)}</p>
					{/if}
				{/if}
			</div>
			<p class="mt-4 text-center text-xs text-zinc-600">powered by oalgosolana · funds go directly to the merchant</p>
		{:else}
			<p class="text-center text-zinc-500">loading checkout…</p>
		{/if}
	</div>
</div>
