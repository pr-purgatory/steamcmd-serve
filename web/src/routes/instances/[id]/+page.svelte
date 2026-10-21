<script lang="ts">
	import { page } from '$app/state';
	import { resolve } from '$app/paths';
	import { onMount } from 'svelte';
	import { api, errorMessage } from '#lib/api/client.ts';
	import type { Game, Instance, InstanceAction } from '#lib/api/types.ts';
	import StateBadge from '#lib/StateBadge.svelte';
	import { canInstall, canRestart, canStart, canStop, canUpdate } from '#lib/states.ts';
	import { formatPort } from '#lib/ports.ts';

	const POLL_MS = 5000;
	const id = $derived(page.params.id ?? '');

	let inst = $state<Instance | null>(null);
	let game = $state<Game | null>(null);
	let error = $state<string | null>(null);
	let busy = $state(false);
	let notFound = $state(false);

	async function refresh() {
		if (!id) return;
		const { data, error: err, response } = await api.GET('/instances/{instanceId}', {
			params: { path: { instanceId: id } }
		});
		if (data) {
			inst = data;
			notFound = false;
			if (!game || game.id !== data.gameId) {
				const g = await api.GET('/games');
				game = g.data?.find((x) => x.id === data.gameId) ?? null;
			}
		} else if (response.status === 404) {
			notFound = true;
		} else if (response.status !== 401) {
			error = errorMessage(err);
		}
	}

	async function act(action: InstanceAction) {
		busy = true;
		error = null;
		const { error: err, response } = await api.POST('/instances/{instanceId}/actions/{action}', {
			params: { path: { instanceId: id, action } }
		});
		if (!response.ok && response.status !== 401) error = `${action}: ${errorMessage(err)}`;
		busy = false;
		await refresh();
	}

	onMount(() => {
		void refresh();
		const t = setInterval(() => {
			if (document.visibilityState === 'visible') void refresh();
		}, POLL_MS);
		return () => clearInterval(t);
	});

	const actions: { action: InstanceAction; label: string; enabled: (i: Instance) => boolean }[] = [
		{ action: 'install', label: 'Install', enabled: (i) => canInstall(i.state) },
		{ action: 'update', label: 'Update', enabled: (i) => canUpdate(i.state) },
		{ action: 'start', label: 'Start', enabled: (i) => canStart(i.state) },
		{ action: 'stop', label: 'Stop', enabled: (i) => canStop(i.state) },
		{ action: 'restart', label: 'Restart', enabled: (i) => canRestart(i.state) }
	];
	const sections = ['Settings', 'Logs', 'Backups', 'Mods'];
</script>

<svelte:head><title>{inst?.name ?? 'Instance'} - steamcmd-serve</title></svelte:head>

<p><a href={resolve('/')}>&larr; All instances</a></p>

{#if notFound}
	<p class="alert" role="alert">Instance not found.</p>
{:else if !inst}
	<p>Loading…</p>
{:else}
	<h1>{inst.name}</h1>
	{#if error}<p class="alert" role="alert">{error}</p>{/if}

	<section class="card" aria-labelledby="details">
		<h2 id="details">Details</h2>
		<dl>
			<dt>State</dt>
			<dd>
				<StateBadge state={inst.state} />
				{#if inst.stateDetail}<span class="hint">{inst.stateDetail}</span>{/if}
			</dd>
			<dt>Ready</dt>
			<dd>{inst.ready ? 'Yes' : 'No'}</dd>
			<dt>Game</dt>
			<dd>{game?.name ?? inst.gameId}</dd>
			<dt>Ports</dt>
			<dd>
				{#each inst.ports as p (p.name)}
					<div><code>{formatPort(p)}</code> <span class="hint">{p.name}</span></div>
				{/each}
			</dd>
			<dt>Memory</dt>
			<dd>{inst.memoryMB} MB</dd>
			<dt>Installed build</dt>
			<dd>{inst.installedBuildId ?? 'Not installed'}</dd>
			{#if inst.configDrift}
				<dt>Config drift</dt>
				<dd>The game rewrote its config since the last render.</dd>
			{/if}
			<dt>Created</dt>
			<dd>{new Date(inst.createdAt).toLocaleString()}</dd>
			<dt>ID</dt>
			<dd><code>{inst.id}</code></dd>
		</dl>
	</section>

	<section class="card" aria-labelledby="actions">
		<h2 id="actions">Actions</h2>
		<div class="row">
			{#each actions as a (a.action)}
				<button type="button" disabled={busy || !a.enabled(inst)} onclick={() => act(a.action)}>
					{a.label}
				</button>
			{/each}
		</div>
	</section>

	<section class="card" aria-labelledby="more">
		<h2 id="more">More</h2>
		<ul class="row more">
			{#each sections as s (s)}
				<li><span class="placeholder" aria-disabled="true">{s} (coming soon)</span></li>
			{/each}
		</ul>
	</section>
{/if}

<style>
	dl {
		display: grid;
		grid-template-columns: max-content 1fr;
		gap: 0.5rem 1.25rem;
		margin: 0;
	}
	dt {
		color: var(--muted);
	}
	dd {
		margin: 0;
	}
	@media (max-width: 30rem) {
		dl {
			grid-template-columns: 1fr;
			gap: 0;
		}
		dd {
			margin-bottom: 0.5rem;
		}
	}
	.more {
		list-style: none;
		padding: 0;
		margin: 0;
	}
	.placeholder {
		color: var(--muted);
		border: 1px dashed var(--border);
		border-radius: var(--radius);
		padding: 0.4rem 0.8rem;
		display: inline-block;
	}
</style>
