<script lang="ts">
	import '../app.css';
	let { children } = $props();
</script>

<div class="min-h-screen bg-zinc-950 text-zinc-100">
	<header class="border-b border-zinc-800">
		<div class="mx-auto flex max-w-5xl items-center justify-between px-6 py-4">
			<a href="/" class="text-lg font-semibold tracking-tight">oalgosolana <span class="text-zinc-500">· invoices</span></a>
			<nav class="flex gap-6 text-sm">
				<a href="/" class="hover:text-white text-zinc-400">New invoice</a>
				<a href="/invoices" class="hover:text-white text-zinc-400">Payments</a>
			</nav>
		</div>
	</header>
	<main class="mx-auto max-w-5xl px-6 py-10">
		{@render children()}
	</main>
</div>
