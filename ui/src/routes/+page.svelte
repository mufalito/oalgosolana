<script lang="ts">
	import { gateway, currencyLabel, STATUS_COLORS, type PaymentCreated, type PaymentStatus } from '#lib/gateway';

	let amount = $state('1.50');
	let currency = $state('SOL');
	let customMint = $state('');
	let orderId = $state('');
	let label = $state('');
	let message = $state('');
	let webhookUrl = $state('');
	let expiration = $state(15);
	let busy = $state(false);
	let error = $state('');
	let created = $state<PaymentCreated | null>(null);
	let status = $state<PaymentStatus | null>(null);
	let copied = $state('');
	let pollTimer = $state<ReturnType<typeof setInterval> | null>(null);

	const effectiveCurrency = () => (currency === 'CUSTOM' ? customMint.trim() : currency);

	async function create() {
		error = '';
		busy = true;
		created = null;
		status = null;
		try {
			created = await gateway.create({
				amount,
				currency: effectiveCurrency() || 'SOL',
				order_id: orderId || `order-${Date.now()}`,
				label: label || undefined,
				message: message || undefined,
				webhook_url: webhookUrl || undefined,
				expiration_minutes: expiration
			});
			poll(created.payment_id);
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		} finally {
			busy = false;
		}
	}

	function poll(id: string) {
		if (pollTimer) clearInterval(pollTimer);
		pollTimer = setInterval(async () => {
			try {
				status = await gateway.get(id);
				if (status.status === 'finalized' || status.status === 'expired') {
					clearInterval(pollTimer!);
					pollTimer = null;
				}
			} catch {}
		}, 3000);
	}

	async function copy(text: string, what: string) {
		await navigator.clipboard.writeText(text);
		copied = what;
		setTimeout(() => (copied = ''), 1200);
	}

	$effect(() => () => pollTimer && clearInterval(pollTimer));
</script>

<div class="grid gap-10 lg:grid-cols-2">
	<section>
		<h1 class="mb-6 text-xl font-semibold">New invoice</h1>
		<form onsubmit={(e) => { e.preventDefault(); create(); }} class="space-y-4">
			<div class="grid grid-cols-2 gap-4">
				<label class="block text-sm">
					Amount (SOL or token units)
					<input bind:value={amount} class="mt-1 w-full rounded-md border border-zinc-700 bg-zinc-900 px-3 py-2 text-base" placeholder="1.50" required />
				</label>
				<label class="block text-sm">
					Expiration (minutes)
					<input type="number" bind:value={expiration} min="1" max="1440" class="mt-1 w-full rounded-md border border-zinc-700 bg-zinc-900 px-3 py-2 text-base" />
				</label>
			</div>

			<fieldset class="text-sm">
				<legend class="mb-1">Currency</legend>
				<div class="flex flex-wrap gap-3">
					<label class="flex items-center gap-2 rounded-md border border-zinc-700 bg-zinc-900 px-3 py-2">
						<input type="radio" bind:group={currency} value="SOL" /> SOL
					</label>
					<label class="flex items-center gap-2 rounded-md border border-zinc-700 bg-zinc-900 px-3 py-2">
						<input type="radio" bind:group={currency} value="CUSTOM" /> SPL mint…
					</label>
					{#if currency === 'CUSTOM'}
						<input bind:value={customMint} class="flex-1 rounded-md border border-zinc-700 bg-zinc-900 px-3 py-2 font-mono text-xs" placeholder="base58 mint (must be allowlisted on the gateway)" />
					{/if}
				</div>
			</fieldset>

			<label class="block text-sm">
				Order ID
				<input bind:value={orderId} class="mt-1 w-full rounded-md border border-zinc-700 bg-zinc-900 px-3 py-2 text-base" placeholder="order-0001 (auto)" />
			</label>
			<div class="grid grid-cols-2 gap-4">
				<label class="block text-sm">
					Label
					<input bind:value={label} class="mt-1 w-full rounded-md border border-zinc-700 bg-zinc-900 px-3 py-2 text-base" placeholder="Coffee Shop" />
				</label>
				<label class="block text-sm">
					Message
					<input bind:value={message} class="mt-1 w-full rounded-md border border-zinc-700 bg-zinc-900 px-3 py-2 text-base" placeholder="Order #0001" />
				</label>
			</div>
			<label class="block text-sm">
				Webhook URL (optional, https only)
				<input bind:value={webhookUrl} class="mt-1 w-full rounded-md border border-zinc-700 bg-zinc-900 px-3 py-2 text-base" placeholder="https://merchant.example.com/webhooks/solanapay" />
			</label>

			{#if error}
				<p class="rounded-md border border-red-800 bg-red-950/50 px-3 py-2 text-sm text-red-300">{error}</p>
			{/if}

			<button type="submit" disabled={busy} class="rounded-md bg-emerald-600 px-4 py-2 font-medium text-white hover:bg-emerald-500 disabled:opacity-50">
				{busy ? 'Creating…' : 'Create invoice'}
			</button>
		</form>
	</section>

	<section>
		{#if created}
			<h2 class="mb-4 text-xl font-semibold">Invoice ready</h2>
			<div class="flex flex-col items-center gap-4 rounded-xl border border-zinc-800 bg-zinc-900/50 p-6">
				<img src="data:image/png;base64,{created.qr_code}" alt="Scan to pay" class="h-56 w-56 rounded-lg bg-white p-2" />
				<p class="text-center text-sm text-zinc-400">Send exactly</p>
				<p class="text-2xl font-semibold">{created.amount} <span class="text-zinc-400">{currencyLabel(created.currency)}</span></p>
				<p class="text-zinc-400">to</p>
				<div class="flex items-center gap-2">
					<code class="rounded-md border border-zinc-700 bg-zinc-900 px-3 py-1.5 text-xs">{created.deposit_address}</code>
					<button onclick={() => copy(created!.deposit_address, 'address')} class="rounded-md border border-zinc-700 px-2 py-1.5 text-xs hover:bg-zinc-800">
						{copied === 'address' ? '✓' : 'copy'}
					</button>
				</div>
				<p class="text-xs text-zinc-500">Any wallet or exchange · plain send, no memo needed</p>
				{#if status}
					<p class="mt-2 text-sm">
						Status: <span class="font-medium {STATUS_COLORS[status.status]}">{status.status}</span>
						{#if status.status === 'finalized'}✓ funds swept to merchant{/if}
					</p>
				{:else}
					<p class="mt-2 text-sm text-zinc-500">waiting for payment…</p>
				{/if}
			</div>
		{:else}
			<div class="flex h-full min-h-64 items-center justify-center rounded-xl border border-dashed border-zinc-800 text-zinc-600">
				Create an invoice to see the payment address and QR
			</div>
		{/if}
	</section>
</div>
