<script lang="ts">
	import { page } from '$app/state';
	import { resolve } from '$app/paths';
	import { beforeNavigate } from '$app/navigation';
	import { onMount } from 'svelte';
	import { api, errorMessage } from '#lib/api/client.ts';
	import type { Instance } from '#lib/api/types.ts';
	import SettingsForm from '#lib/config-form/SettingsForm.svelte';
	import {
		sameJson,
		toFormValue,
		toPayload,
		validateForm,
		type Json,
		type Schema
	} from '#lib/config-form/logic.ts';

	const id = $derived(page.params.id ?? '');

	let inst = $state<Instance | null>(null);
	let schema = $state<Schema | null>(null);
	let original = $state<Json>({}); // as returned by the server (secrets masked)
	let form = $state<Record<string, Json>>({});
	let loadError = $state<string | null>(null);
	let notFound = $state(false);
	let saveError = $state<string | null>(null);
	let saved = $state(false);
	let busy = $state(false);

	const errors = $derived(schema ? validateForm(schema, form, original) : {});
	const errorCount = $derived(Object.keys(errors).length);
	const payload = $derived(schema ? toPayload(schema, form, original) : null);
	const dirty = $derived(payload !== null && !sameJson(payload, original));
	const rootError = $derived(errors['']);

	onMount(() => {
		void load();
	});

	async function load() {
		const i = await api.GET('/instances/{instanceId}', { params: { path: { instanceId: id } } });
		if (!i.data) {
			if (i.response.status === 404) notFound = true;
			else if (i.response.status !== 401) loadError = errorMessage(i.error);
			return;
		}
		const [g, s] = await Promise.all([
			api.GET('/games/{gameId}', { params: { path: { gameId: i.data.gameId } } }),
			api.GET('/instances/{instanceId}/settings', { params: { path: { instanceId: id } } })
		]);
		if (!g.data || !s.data) {
			loadError = errorMessage(g.error ?? s.error, 'Could not load settings');
			return;
		}
		inst = i.data;
		schema = g.data.settingsSchema as Schema;
		adopt(s.data as Json);
	}

	function adopt(server: Json) {
		original = server;
		form = toFormValue(schema as Schema, server) as Record<string, Json>;
	}

	async function save() {
		if (!schema || !dirty || errorCount) return;
		busy = true;
		saveError = null;
		saved = false;
		const { data, error, response } = await api.PUT('/instances/{instanceId}/settings', {
			params: { path: { instanceId: id } },
			body: payload as Record<string, unknown>
		});
		busy = false;
		if (data) {
			adopt(data as Json);
			saved = true;
		} else if (response.status !== 401) {
			saveError = errorMessage(error, 'Could not save settings');
		}
	}

	beforeNavigate(({ cancel, type }) => {
		if (!dirty) return;
		if (type === 'leave') {
			cancel(); // triggers the browser's native unload prompt
			return;
		}
		if (!confirm('You have unsaved changes. Leave this page and discard them?')) cancel();
	});

	function onBeforeUnload(e: BeforeUnloadEvent) {
		if (dirty) e.preventDefault();
	}
</script>

<svelte:window onbeforeunload={onBeforeUnload} />
<svelte:head><title>Settings - {inst?.name ?? 'Instance'} - steamcmd-serve</title></svelte:head>

<p><a href={resolve('/instances/[id]', { id })}>&larr; Back to instance</a></p>

{#if notFound}
	<p class="alert" role="alert">Instance not found.</p>
{:else if loadError}
	<p class="alert" role="alert">{loadError}</p>
{:else if !inst || !schema}
	<p>Loading…</p>
{:else}
	<h1>Settings: {inst.name}</h1>
	<p class="hint">Settings apply on next start.</p>
	{#if saveError}<p class="alert" role="alert">{saveError}</p>{/if}
	{#if saved && !dirty}<p class="ok" role="status">Saved. Changes apply on next start.</p>{/if}

	{#if rootError}<p class="alert" role="alert">{rootError}</p>{/if}

	<form
		onsubmit={(e) => {
			e.preventDefault();
			void save();
		}}
	>
		<SettingsForm {schema} value={form} {original} {errors} onchange={(v) => (form = v)} />

		<div class="row bar">
			<button type="submit" class="primary" disabled={busy || !dirty || errorCount > 0}>
				{busy ? 'Saving…' : 'Save'}
			</button>
			<button type="button" disabled={busy || !dirty} onclick={() => adopt(original)}>
				Discard changes
			</button>
			{#if errorCount > 0}
				<span class="hint" role="status">{errorCount} invalid {errorCount === 1 ? 'field' : 'fields'}</span>
			{:else if dirty}
				<span class="hint">Unsaved changes</span>
			{/if}
		</div>
	</form>
{/if}

<style>
	.hint {
		color: var(--muted);
	}
	.ok {
		color: var(--ok);
	}
	.bar {
		position: sticky;
		bottom: 0;
		background: var(--bg);
		padding: 0.75rem 0;
		align-items: center;
	}
</style>
