<script lang="ts">
	import '../app.css';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { resolve } from '$app/paths';
	import { api, onUnauthorized, setupRequired } from '#lib/api/client.ts';

	let { children } = $props();

	let username = $state<string | null>(null);
	const bare = $derived(page.url.pathname === '/login' || page.url.pathname === '/setup');

	onUnauthorized(() => {
		username = null;
		if (page.url.pathname !== '/login' && page.url.pathname !== '/setup') {
			void setupRequired().then((req) => goto(resolve(req ? '/setup' : '/login')));
		}
	});

	$effect(() => {
		// Re-check the session whenever we leave a bare page (e.g. after login).
		if (!bare && username === null) {
			void api.GET('/auth/session').then(({ data }) => {
				if (data) username = data.username;
			});
		}
	});

	async function logout() {
		await api.POST('/auth/logout');
		username = null;
		await goto(resolve('/login'));
	}
</script>

{#if !bare}
	<header class="topbar">
		<nav aria-label="Main">
			<a class="brand" href={resolve('/')}><img src="/logo-64.png" alt="" width="28" height="28" />steamcmd-serve</a>
			<a href={resolve('/')} aria-current={page.url.pathname === '/' ? 'page' : undefined}>Instances</a>
			<a
				href={resolve('/instances/new')}
				aria-current={page.url.pathname === '/instances/new' ? 'page' : undefined}>New instance</a
			>
			<a
				href={resolve('/settings')}
				aria-current={page.url.pathname === '/settings' ? 'page' : undefined}>Settings</a
			>
			<span class="spacer"></span>
			{#if username}
				<span class="who">{username}</span>
				<button type="button" onclick={logout}>Log out</button>
			{/if}
		</nav>
	</header>
{/if}

<main>
	{@render children()}
</main>

<style>
	.topbar {
		background: var(--surface);
		border-bottom: 1px solid var(--border);
	}
	nav {
		max-width: 64rem;
		margin: 0 auto;
		padding: 0.5rem 1rem;
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem 1rem;
		align-items: center;
	}
	.brand {
		display: inline-flex;
		align-items: center;
		gap: 0.5rem;
		font-weight: 700;
		color: var(--text);
		text-decoration: none;
	}
	a[aria-current='page'] {
		font-weight: 700;
		text-decoration: none;
		border-bottom: 2px solid var(--accent);
	}
	.spacer {
		flex: 1;
	}
	.who {
		color: var(--muted);
	}
</style>
