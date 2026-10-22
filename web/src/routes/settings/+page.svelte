<script lang="ts">
	import { onMount } from 'svelte';
	import { api, errorMessage } from '#lib/api/client.ts';
	import type { NexusAccount } from '#lib/api/types.ts';
	import { nexusStatus, putNexusKey } from '#lib/mods.ts';

	let account = $state<NexusAccount | null>(null);
	let loaded = $state(false);
	// The key lives only in this input while typing; it is cleared right after saving.
	let keyInput = $state('');
	let saving = $state(false);
	let removing = $state(false);
	let confirmRemove = $state(false);
	let error = $state<string | null>(null);
	let ok = $state<string | null>(null);

	async function load() {
		const { data, error: err, response } = await api.GET('/settings/nexus-api-key');
		if (data) account = data;
		else if (response.status !== 401) error = errorMessage(err, 'Could not load NexusMods status');
		loaded = true;
	}

	async function save(e: SubmitEvent) {
		e.preventDefault();
		if (!keyInput.trim() || saving) return;
		saving = true;
		error = null;
		ok = null;
		const { data, error: err, response } = await putNexusKey(api, keyInput.trim());
		saving = false;
		if (data) {
			account = data;
			keyInput = '';
			ok = 'NexusMods API key saved.';
		} else if (response.status !== 401) {
			error = errorMessage(err, 'Could not save the key');
		}
	}

	async function remove() {
		removing = true;
		error = null;
		ok = null;
		const { error: err, response } = await api.DELETE('/settings/nexus-api-key');
		removing = false;
		confirmRemove = false;
		if (response.ok) {
			account = { hasValue: false };
			ok = 'NexusMods API key removed.';
		} else if (response.status !== 401) {
			error = errorMessage(err, 'Could not remove the key');
		}
	}

	onMount(() => void load());
</script>

<svelte:head><title>Settings - steamcmd-serve</title></svelte:head>

<h1>Settings</h1>

<section class="card" aria-labelledby="nexus-h">
	<h2 id="nexus-h">NexusMods</h2>
	{#if error}<p class="alert" role="alert">{error}</p>{/if}
	{#if ok}<p class="ok" role="status">{ok}</p>{/if}

	<p>
		Status:
		<strong>{loaded ? nexusStatus(account) : 'Loading…'}</strong>
	</p>
	<p class="hint">
		Free NexusMods accounts cannot download mod files through the API: you download the file manually
		on nexusmods.com and upload it on the instance's Mods page. Premium accounts install directly.
		The key is stored on the server and is never shown again.
	</p>

	<form onsubmit={save} autocomplete="off">
		<label>
			{account?.hasValue ? 'Replace API key' : 'API key'}
			<input
				type="password"
				name="nexus-api-key"
				bind:value={keyInput}
				autocomplete="off"
				spellcheck="false"
				required
			/>
		</label>
		<div class="row">
			<button type="submit" class="primary" disabled={saving || !keyInput.trim()}>
				{saving ? 'Saving…' : 'Save key'}
			</button>
		</div>
	</form>

	{#if account?.hasValue}
		{#if !confirmRemove}
			<div class="row">
				<button type="button" onclick={() => (confirmRemove = true)} disabled={removing}>Remove key</button>
			</div>
		{:else}
			<div role="alertdialog" aria-label="Confirm remove key" class="confirm">
				<p>Remove the NexusMods API key? You will not be able to search NexusMods until you add one.</p>
				<div class="row">
					<button type="button" class="danger" disabled={removing} onclick={remove}>Confirm remove</button>
					<button type="button" onclick={() => (confirmRemove = false)}>Cancel</button>
				</div>
			</div>
		{/if}
	{/if}
</section>

<style>
	.hint {
		color: var(--muted);
	}
	.ok {
		color: var(--ok);
	}
	.confirm {
		background: var(--busy-bg);
		padding: 0.5rem;
		margin-top: 0.5rem;
	}
	label {
		display: block;
	}
</style>
