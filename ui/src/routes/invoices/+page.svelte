<script lang="ts">
	import { gateway, currencyLabel, STATUS_COLORS, type PaymentStatus } from '#lib/gateway';

	let payments = $state<PaymentStatus[]>([]);
	let error = $state('');
	let note = $state('');

	async function refresh() {
		try {
			payments = await gateway.list();
			error = '';
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
	}

	async function replay(id: string) {
		try {
			await gateway.replay(id);
			note = `replay queued for ${id.slice(0, 8)}…`;
			setTimeout(() => (note = ''), 2500);
		} catch (e) {
			note = e instanceof Error ? e.message : String(e);
		}
	}

	$effect(() => {
		refresh();
		const t = setInterval(refresh, 5000);
		return () => clearInterval(t);
	});
</script>

<div class="flex items-center justify-between">
	<h1 class="text-xl font-semibold">Payments</h1>
	<button onclick={refresh} class="rounded-md border border-zinc-700 px-3 py-1.5 text-sm hover:bg-zinc-800">refresh</button>
</div>

{#if note}<p class="mt-3 text-sm text-emerald-400">{note}</p>{/if}
{#if error}<p class="mt-3 text-sm text-red-400">{error}</p>{/if}

<div class="mt-6 overflow-x-auto rounded-xl border border-zinc-800">
	<table class="w-full text-sm">
		<thead class="border-b border-zinc-800 text-left text-zinc-400">
			<tr>
				<th class="px-4 py-3">Order</th>
				<th class="px-4 py-3">Amount</th>
				<th class="px-4 py-3">Status</th>
				<th class="px-4 py-3">Deposit address</th>
				<th class="px-4 py-3">Expires</th>
				<th class="px-4 py-3"></th>
			</tr>
		</thead>
		<tbody>
			{#each payments as p (p.payment_id)}
				<tr class="border-b border-zinc-800/50 hover:bg-zinc-900/50">
					<td class="px-4 py-3 font-mono text-xs">{p.order_id}</td>
					<td class="px-4 py-3 whitespace-nowrap">{p.amount} <span class="text-zinc-500">{currencyLabel(p.currency)}</span></td>
					<td class="px-4 py-3 font-medium {STATUS_COLORS[p.status]}">{p.status}</td>
					<td class="px-4 py-3 font-mono text-xs text-zinc-400">{p.deposit_address.slice(0, 8)}…{p.deposit_address.slice(-6)}</td>
					<td class="px-4 py-3 text-xs text-zinc-500">{new Date(p.expires_at).toLocaleTimeString()}</td>
					<td class="px-4 py-3 text-right">
						{#if p.status === 'confirmed' || p.status === 'finalized'}
							<button onclick={() => replay(p.payment_id)} class="rounded-md border border-zinc-700 px-2 py-1 text-xs hover:bg-zinc-800">replay webhook</button>
						{/if}
					</td>
				</tr>
			{:else}
				<tr><td colspan="6" class="px-4 py-8 text-center text-zinc-600">no payments yet</td></tr>
			{/each}
		</tbody>
	</table>
</div>
<p class="mt-3 text-xs text-zinc-600">auto-refreshes every 5s · most recent 50</p>
