<script lang="ts">
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import { onMount } from 'svelte';
	import { api, errorMessage } from '#lib/api/client.ts';
	import type { Game, Instance, InstanceAction, Job } from '#lib/api/types.ts';
	import StateBadge from '#lib/StateBadge.svelte';
	import {
		canEditInstance,
		canInstall,
		canRestart,
		canStart,
		canStop,
		canUpdate
	} from '#lib/states.ts';
	import {
		NAME_MAX,
		createPortsPayload,
		formatPort,
		resolvePorts,
		validateMemory,
		validateName,
		validatePorts
	} from '#lib/ports.ts';
	import { isAbortError, pollJob } from '#lib/jobs.ts';
	import { takeCreatedWarnings } from '#lib/created.ts';

	const POLL_MS = 5000;
	const id = $derived(page.params.id ?? '');

	let inst = $state<Instance | null>(null);
	let game = $state<Game | null>(null);
	let error = $state<string | null>(null);
	let notFound = $state(false);
	let warnings = $state<string[]>([]);

	// action + job
	let busy = $state(false);
	let job = $state<Job | null>(null);
	let jobLabel = $state('');
	let jobDone = $state<string | null>(null);
	let ac: AbortController | null = null;

	// edit
	let editing = $state(false);
	let editName = $state('');
	let editMemory = $state('');
	let editPorts = $state<Record<string, string>>({});
	let editError = $state<string | null>(null);
	let saving = $state(false);

	// delete
	let confirmingDelete = $state(false);
	let deleteData = $state(false);
	let deleting = $state(false);
	let deleteError = $state<string | null>(null);

	const idle = $derived(inst ? canEditInstance(inst.state) : false);
	const editNameError = $derived(validateName(editName));
	const editMemoryError = $derived(validateMemory(editMemory));
	const editPortErrors = $derived(game ? validatePorts(game.ports, editPorts) : {});
	const editRows = $derived(game ? resolvePorts(game.ports, editPorts) : []);
	const editValid = $derived(
		!!game && !editNameError && !editMemoryError && Object.keys(editPortErrors).length === 0
	);

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
		if (busy) return;
		busy = true;
		error = null;
		jobDone = null;
		job = null;
		jobLabel = ACTION_LABEL[action];
		ac?.abort();
		const mine = new AbortController();
		ac = mine;
		try {
			const { data, error: err, response } = await api.POST('/instances/{instanceId}/actions/{action}', {
				params: { path: { instanceId: id, action } }
			});
			if (!data) {
				if (response.status !== 401) error = `${jobLabel}: ${errorMessage(err)}`;
				return;
			}
			const jobId = data.id;
			job = data;
			await pollJob(
				async () => {
					const r = await api.GET('/jobs/{jobId}', { params: { path: { jobId } } });
					if (!r.data) throw new Error(errorMessage(r.error, 'Could not read job status'));
					return r.data;
				},
				{ intervalMs: 1000, signal: mine.signal, onUpdate: (j) => (job = j) }
			);
			jobDone = `${jobLabel}: done.`;
		} catch (e) {
			if (!isAbortError(e)) error = `${jobLabel}: ${e instanceof Error ? e.message : String(e)}`;
		} finally {
			if (ac === mine) {
				busy = false;
				job = null;
				if (!mine.signal.aborted) await refresh();
			}
		}
	}

	function startEdit() {
		if (!inst) return;
		editName = inst.name;
		editMemory = String(inst.memoryMB);
		editPorts = Object.fromEntries(
			(game?.ports ?? [])
				.filter((s) => !s.derivedFrom)
				.map((s) => [s.name, String(inst?.ports.find((p) => p.name === s.name)?.port ?? s.default)])
		);
		editError = null;
		editing = true;
	}

	async function saveEdit(e: SubmitEvent) {
		e.preventDefault();
		if (!game || !editValid || saving) return;
		saving = true;
		editError = null;
		const { data, error: err, response } = await api.PATCH('/instances/{instanceId}', {
			params: { path: { instanceId: id } },
			body: {
				name: editName,
				memoryMB: Number(editMemory),
				ports: createPortsPayload(game.ports, editPorts)
			}
		});
		saving = false;
		if (data) {
			inst = data;
			editing = false;
		} else if (response.status !== 401) {
			editError = errorMessage(err, `Save failed (${response.status})`);
		}
	}

	async function doDelete() {
		if (deleting) return;
		deleting = true;
		deleteError = null;
		const { error: err, response } = await api.DELETE('/instances/{instanceId}', {
			params: { path: { instanceId: id }, query: { deleteData } }
		});
		deleting = false;
		if (response.ok) {
			ac?.abort();
			await goto(resolve('/'));
		} else if (response.status !== 401) {
			deleteError = errorMessage(err, `Delete failed (${response.status})`);
		}
	}

	onMount(() => {
		if (page.url.searchParams.get('created') === '1') warnings = takeCreatedWarnings(id);
		void refresh();
		const t = setInterval(() => {
			if (document.visibilityState === 'visible') void refresh();
		}, POLL_MS);
		return () => {
			clearInterval(t);
			ac?.abort();
		};
	});

	const ACTION_LABEL: Record<InstanceAction, string> = {
		install: 'Install',
		update: 'Update',
		start: 'Start',
		stop: 'Stop',
		restart: 'Restart'
	};
	const actions: { action: InstanceAction; enabled: (i: Instance) => boolean }[] = [
		{ action: 'install', enabled: (i) => canInstall(i.state) },
		{ action: 'update', enabled: (i) => canUpdate(i.state) },
		{ action: 'start', enabled: (i) => canStart(i.state) },
		{ action: 'stop', enabled: (i) => canStop(i.state) },
		{ action: 'restart', enabled: (i) => canRestart(i.state) }
	];
</script>

<svelte:head><title>{inst?.name ?? 'Instance'} - steamcmd-serve</title></svelte:head>

<p><a href={resolve('/')}>&larr; All instances</a></p>

{#if notFound}
	<p class="alert" role="alert">Instance not found.</p>
{:else if !inst}
	<p>Loading…</p>
{:else}
	<h1>{inst.name}</h1>
	{#if warnings.length}
		<div class="notice" role="status">
			<p><strong>Instance created, with warnings:</strong></p>
			<ul>
				{#each warnings as w (w)}<li>{w}</li>{/each}
			</ul>
			<button type="button" onclick={() => (warnings = [])}>Dismiss</button>
		</div>
	{/if}
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
					{ACTION_LABEL[a.action]}
				</button>
			{/each}
		</div>
		{#if job}
			<p class="job" role="status">
				{jobLabel}: {job.status}{job.message ? ` - ${job.message}` : ''}
				<progress max="1" value={job.progress ?? 0} aria-label="{jobLabel} progress"></progress>
				{Math.round((job.progress ?? 0) * 100)}%
			</p>
		{/if}
		{#if jobDone}<p class="ok" role="status">{jobDone}</p>{/if}
	</section>

	<section class="card" aria-labelledby="manage">
		<h2 id="manage">Edit and delete</h2>
		{#if !idle}
			<p class="hint">Editing and deleting are available only while the instance is new, stopped or in error.</p>
		{:else if editing}
			{#if editError}<p class="alert" role="alert">{editError}</p>{/if}
			<form onsubmit={saveEdit} novalidate>
				<div class="field">
					<label for="edit-name">Instance name</label>
					<input
						id="edit-name"
						bind:value={editName}
						maxlength={NAME_MAX}
						autocomplete="off"
						aria-invalid={editNameError ? 'true' : undefined}
						aria-describedby={editNameError ? 'edit-name-err' : undefined}
					/>
					{#if editNameError}<p class="field-error" id="edit-name-err">{editNameError}</p>{/if}
				</div>
				{#each editRows as row (row.name)}
					{@const perr = editPortErrors[row.name]}
					<div class="field">
						<label for="edit-port-{row.name}">
							{row.name} port ({row.protocol}){row.derived ? ' - derived' : ''}
						</label>
						{#if row.derived}
							<input
								id="edit-port-{row.name}"
								value={row.port === null ? '' : String(row.port)}
								readonly
								aria-invalid={perr ? 'true' : undefined}
								aria-describedby="edit-port-{row.name}-hint{perr ? ` edit-port-${row.name}-err` : ''}"
							/>
							<p class="hint" id="edit-port-{row.name}-hint">
								{row.derivedFrom} + {row.offset}{row.port === null ? '' : ` = ${row.port}/${row.protocol}`}.
								Read-only.
							</p>
						{:else}
							<input
								id="edit-port-{row.name}"
								inputmode="numeric"
								autocomplete="off"
								bind:value={editPorts[row.name]}
								aria-invalid={perr ? 'true' : undefined}
								aria-describedby={perr ? `edit-port-${row.name}-err` : undefined}
							/>
						{/if}
						{#if perr}<p class="field-error" id="edit-port-{row.name}-err">{perr}</p>{/if}
					</div>
				{/each}
				<div class="field">
					<label for="edit-memory">Memory limit (MB)</label>
					<input
						id="edit-memory"
						inputmode="numeric"
						bind:value={editMemory}
						aria-invalid={editMemoryError ? 'true' : undefined}
						aria-describedby={editMemoryError ? 'edit-mem-err' : undefined}
					/>
					{#if editMemoryError}<p class="field-error" id="edit-mem-err">{editMemoryError}</p>{/if}
				</div>
				<div class="row">
					<button type="submit" class="primary" disabled={saving || !editValid}>
						{saving ? 'Saving…' : 'Save changes'}
					</button>
					<button type="button" onclick={() => (editing = false)}>Cancel</button>
				</div>
			</form>
		{:else if confirmingDelete}
			<div role="alertdialog" aria-label="Confirm delete" class="confirm">
				<p>Delete <strong>{inst.name}</strong>? This cannot be undone.</p>
				{#if deleteError}<p class="alert" role="alert">{deleteError}</p>{/if}
				<div class="field check">
					<input id="delete-data" type="checkbox" bind:checked={deleteData} />
					<label for="delete-data">Also delete game files, saves and backups</label>
				</div>
				<div class="row">
					<button type="button" class="danger" disabled={deleting} onclick={doDelete}>
						{deleting ? 'Deleting…' : 'Confirm delete'}
					</button>
					<button
						type="button"
						onclick={() => {
							confirmingDelete = false;
							deleteError = null;
						}}>Cancel</button
					>
				</div>
			</div>
		{:else}
			<div class="row">
				<button type="button" onclick={startEdit}>Edit</button>
				<button
					type="button"
					class="danger"
					onclick={() => {
						deleteData = false;
						deleteError = null;
						confirmingDelete = true;
					}}>Delete…</button
				>
			</div>
		{/if}
	</section>

	<section class="card" aria-labelledby="more">
		<h2 id="more">More</h2>
		<ul class="row more">
			<li><a class="linkbtn" href={resolve('/instances/[id]/settings', { id })}>Settings</a></li>
			<li><a class="linkbtn" href={resolve('/instances/[id]/logs', { id })}>Logs</a></li>
			<li><a class="linkbtn" href={resolve('/instances/[id]/backups', { id })}>Backups</a></li>
			<li><a class="linkbtn" href={resolve('/instances/[id]/mods', { id })}>Mods</a></li>
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
	.ok {
		color: var(--ok);
	}
	.job {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
		align-items: center;
	}
	.notice {
		border: 1px solid var(--border);
		border-left: 4px solid var(--accent);
		background: var(--surface);
		border-radius: var(--radius);
		padding: 0.5rem 0.75rem;
		margin-bottom: 1rem;
	}
	.notice ul {
		margin: 0.25rem 0 0.5rem;
	}
	.check {
		display: flex;
		gap: 0.5rem;
		align-items: center;
	}
	.check input {
		width: auto;
	}
	.more {
		list-style: none;
		padding: 0;
		margin: 0;
	}
	.linkbtn {
		border: 1px solid var(--border);
		border-radius: var(--radius);
		padding: 0.4rem 0.8rem;
		display: inline-block;
		text-decoration: none;
	}
</style>
