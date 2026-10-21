<script lang="ts">
	import { resolve } from '$app/paths';
	import { onMount } from 'svelte';
	import { api, errorMessage } from '#lib/api/client.ts';
	import type { Game, Instance, InstanceAction } from '#lib/api/types.ts';
	import StateBadge from '#lib/StateBadge.svelte';
	import { canStart, canStop } from '#lib/states.ts';
	import { formatPort } from '#lib/ports.ts';

	const POLL_MS = 5000;

	let instances = $state<Instance[] | null>(null);
	let games = $state<Game[]>([]);
	let error = $state<string | null>(null);
	let pending = $state<Record<string, boolean>>({});

	const gameName = (id: string) => games.find((g) => g.id === id)?.name ?? id;

	async function refresh() {
		const { data, error: err, response } = await api.GET('/instances');
		if (data) {
			instances = data;
			error = null;
		} else if (response.status !== 401) {
			error = errorMessage(err, 'Could not load instances');
		}
	}

	async function act(inst: Instance, action: InstanceAction) {
		pending[inst.id] = true;
		error = null;
		const { error: err, response } = await api.POST('/instances/{instanceId}/actions/{action}', {
			params: { path: { instanceId: inst.id, action } }
		});
		if (!response.ok && response.status !== 401) {
			error = `${action} ${inst.name}: ${errorMessage(err)}`;
		}
		pending[inst.id] = false;
		await refresh();
	}

	onMount(() => {
		void api.GET('/games').then(({ data }) => {
			if (data) games = data;
		});
		void refresh();

		let timer: ReturnType<typeof setInterval> | undefined;
		const start = () => {
			if (timer === undefined) timer = setInterval(() => void refresh(), POLL_MS);
		};
		const stop = () => {
			if (timer !== undefined) clearInterval(timer);
			timer = undefined;
		};
		const onVisibility = () => {
			if (document.visibilityState === 'visible') {
				void refresh();
				start();
			} else {
				stop();
			}
		};
		document.addEventListener('visibilitychange', onVisibility);
		if (document.visibilityState === 'visible') start();

		return () => {
			stop();
			document.removeEventListener('visibilitychange', onVisibility);
		};
	});
</script>

<svelte:head><title>Instances - steamcmd-serve</title></svelte:head>

<div class="row head">
	<h1>Instances</h1>
	<a class="btn primary" href={resolve('/instances/new')}>New instance</a>
</div>

{#if error}<p class="alert" role="alert">{error}</p>{/if}

{#if instances === null}
	<p>Loading…</p>
{:else if instances.length === 0}
	<div class="card">
		<p>No instances yet.</p>
		<a class="btn primary" href={resolve('/instances/new')}>Create your first instance</a>
	</div>
{:else}
	<div class="card table-wrap">
		<table>
			<caption class="sr-only">Game server instances</caption>
			<thead>
				<tr>
					<th scope="col">Name</th>
					<th scope="col">Game</th>
					<th scope="col">State</th>
					<th scope="col">Ready</th>
					<th scope="col">Ports</th>
					<th scope="col">Memory</th>
					<th scope="col">Actions</th>
				</tr>
			</thead>
			<tbody>
				{#each instances as inst (inst.id)}
					<tr>
						<th scope="row">
							<a href={resolve('/instances/[id]', { id: inst.id })}>{inst.name}</a>
						</th>
						<td>{gameName(inst.gameId)}</td>
						<td>
							<StateBadge state={inst.state} />
							{#if inst.state === 'error' && inst.stateDetail}
								<div class="hint">{inst.stateDetail}</div>
							{/if}
						</td>
						<td>
							{#if inst.ready}
								<span aria-hidden="true">✔</span> Ready
							{:else}
								<span aria-hidden="true">–</span> Not ready
							{/if}
						</td>
						<td>
							{#each inst.ports as p (p.name)}
								<div><code>{formatPort(p)}</code> <span class="hint">{p.name}</span></div>
							{/each}
						</td>
						<td>{inst.memoryMB} MB</td>
						<td>
							<div class="row">
								<button
									type="button"
									disabled={!canStart(inst.state) || pending[inst.id]}
									onclick={() => act(inst, 'start')}>Start</button
								>
								<button
									type="button"
									disabled={!canStop(inst.state) || pending[inst.id]}
									onclick={() => act(inst, 'stop')}>Stop</button
								>
							</div>
						</td>
					</tr>
				{/each}
			</tbody>
		</table>
	</div>
{/if}

<style>
	.head {
		justify-content: space-between;
	}
	.head :global(h1) {
		margin: 0;
	}
	.head {
		margin-bottom: 1rem;
	}
</style>
