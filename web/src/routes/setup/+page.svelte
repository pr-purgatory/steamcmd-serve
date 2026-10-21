<script lang="ts">
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import { api, errorMessage } from '#lib/api/client.ts';

	const MIN_PASSWORD = 12;

	let username = $state('');
	let password = $state('');
	let confirm = $state('');
	let error = $state<string | null>(null);
	let busy = $state(false);

	const passwordError = $derived(
		password.length > 0 && password.length < MIN_PASSWORD
			? `At least ${MIN_PASSWORD} characters (${password.length} so far)`
			: null
	);
	const confirmError = $derived(
		confirm.length > 0 && confirm !== password ? 'Passwords do not match' : null
	);
	const valid = $derived(
		username.trim().length > 0 &&
			password.length >= MIN_PASSWORD &&
			confirm === password
	);

	async function submit(e: SubmitEvent) {
		e.preventDefault();
		if (!valid) return;
		error = null;
		busy = true;
		const { response, error: err } = await api.POST('/setup', {
			body: { username: username.trim(), password }
		});
		busy = false;
		if (response.ok) {
			await goto(resolve('/login'));
		} else if (response.status === 409) {
			error = 'Setup is already complete. Log in instead.';
		} else {
			error = errorMessage(err, 'Setup failed');
		}
	}
</script>

<svelte:head><title>Set up - steamcmd-serve</title></svelte:head>

<div class="card narrow">
	<h1>Create the admin account</h1>
	<p class="hint">Only available until the first admin exists.</p>
	{#if error}<p class="alert" role="alert">{error}</p>{/if}
	<form onsubmit={submit}>
		<div class="field">
			<label for="username">Username</label>
			<input id="username" bind:value={username} autocomplete="username" required maxlength="64" />
		</div>
		<div class="field">
			<label for="password">Password (min {MIN_PASSWORD} characters)</label>
			<input
				id="password"
				type="password"
				bind:value={password}
				autocomplete="new-password"
				required
				minlength={MIN_PASSWORD}
				aria-invalid={passwordError ? 'true' : undefined}
				aria-describedby={passwordError ? 'pw-err' : undefined}
			/>
			{#if passwordError}<p class="field-error" id="pw-err">{passwordError}</p>{/if}
		</div>
		<div class="field">
			<label for="confirm">Confirm password</label>
			<input
				id="confirm"
				type="password"
				bind:value={confirm}
				autocomplete="new-password"
				required
				aria-invalid={confirmError ? 'true' : undefined}
				aria-describedby={confirmError ? 'cf-err' : undefined}
			/>
			{#if confirmError}<p class="field-error" id="cf-err">{confirmError}</p>{/if}
		</div>
		<button class="primary" type="submit" disabled={busy || !valid}>
			{busy ? 'Creating…' : 'Create admin'}
		</button>
	</form>
	<p class="hint">Already set up? <a href={resolve('/login')}>Log in</a>.</p>
</div>

<style>
	.narrow {
		max-width: 26rem;
		margin: 2rem auto;
	}
</style>
