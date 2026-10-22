<script lang="ts">
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import { onMount } from 'svelte';
	import { api, errorMessage, setupRequired } from '#lib/api/client.ts';

	let username = $state('');
	let password = $state('');
	let error = $state<string | null>(null);
	let busy = $state(false);

	onMount(() => {
		void setupRequired().then((req) => {
			if (req) void goto(resolve('/setup'));
		});
	});

	async function submit(e: SubmitEvent) {
		e.preventDefault();
		error = null;
		busy = true;
		const { response, error: err } = await api.POST('/auth/login', {
			body: { username, password }
		});
		busy = false;
		if (response.ok) {
			await goto(resolve('/'));
		} else if (response.status === 429) {
			error = 'Too many attempts. Wait a moment and try again.';
		} else if (response.status === 401) {
			error = 'Invalid username or password.';
		} else {
			error = errorMessage(err, 'Login failed');
		}
	}
</script>

<svelte:head><title>Log in - steamcmd-serve</title></svelte:head>

<div class="card narrow">
	<h1>Log in</h1>
	{#if error}<p class="alert" role="alert">{error}</p>{/if}
	<form onsubmit={submit}>
		<div class="field">
			<label for="username">Username</label>
			<input id="username" bind:value={username} autocomplete="username" required maxlength="64" />
		</div>
		<div class="field">
			<label for="password">Password</label>
			<input
				id="password"
				type="password"
				bind:value={password}
				autocomplete="current-password"
				required
			/>
		</div>
		<button class="primary" type="submit" disabled={busy}>{busy ? 'Logging in…' : 'Log in'}</button>
	</form>
	<p class="hint">First run? <a href={resolve('/setup')}>Create the admin account</a>.</p>
</div>

<style>
	.narrow {
		max-width: 26rem;
		margin: 2rem auto;
	}
</style>
