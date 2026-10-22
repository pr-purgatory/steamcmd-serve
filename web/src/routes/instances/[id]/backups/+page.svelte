<script lang="ts">
	import { page } from '$app/state';
	import { resolve } from '$app/paths';
	import { onMount } from 'svelte';
	import { api, errorMessage } from '#lib/api/client.ts';
	import type { Instance, Job } from '#lib/api/types.ts';
	import StateBadge from '#lib/StateBadge.svelte';
	import { canRestore } from '#lib/states.ts';
	import { REASON_LABEL, humanSize, type Backup } from '#lib/backups.ts';
	import { isAbortError, pollJob } from '#lib/jobs.ts';

	const POLL_MS = 5000;
	const id = $derived(page.params.id ?? '');

	let inst = $state<Instance | null>(null);
	let list = $state<Backup[] | null>(null);
	let notFound = $state(false);
	let error = $state<string | null>(null);
	let confirming = $state<string | null>(null); // backup id awaiting confirmation
	let job = $state<Job | null>(null);
	let jobLabel = $state('');
	let jobDone = $state<string | null>(null);
	let working = $state(false);

	let ac: AbortController | null = null;

	const restorable = $derived(inst ? canRestore(inst.state) : false);

	async function refreshInstance() {
		const { data, response } = await api.GET('/instances/{instanceId}', {
			params: { path: { instanceId: id } }
		});
		if (data) inst = data;
		else if (response.status === 404) notFound = true;
	}

	async function refreshList() {
		const { data, error: err, response } = await api.GET('/instances/{instanceId}/backups', {
			params: { path: { instanceId: id } }
		});
		if (data) list = data;
		else if (response.status !== 401) error = errorMessage(err, 'Could not load backups');
	}

	async function run(
		label: string,
		doneText: string,
		start: () => Promise<{ job?: Job; message?: string }>
	) {
		if (working) return;
		working = true;
		error = null;
		jobDone = null;
		job = null;
		jobLabel = label;
		ac?.abort();
		const mine = new AbortController();
		ac = mine;
		try {
			const started = await start();
			if (!started.job) {
				error = started.message ?? `${label} failed`;
				return;
			}
			const jobId = started.job.id;
			job = started.job;
			await pollJob(
				async () => {
					const { data, error: err } = await api.GET('/jobs/{jobId}', {
						params: { path: { jobId } }
					});
					if (!data) throw new Error(errorMessage(err, 'Could not read job status'));
					return data;
				},
				{ intervalMs: 1000, signal: mine.signal, onUpdate: (j) => (job = j) }
			);
						jobDone = doneText;
		} catch (e) {
			if (!isAbortError(e)) error = `${label}: ${e instanceof Error ? e.message : String(e)}`;
		} finally {
			if (ac === mine) {
				working = false;
				job = null;
				if (!mine.signal.aborted) {
					await Promise.all([refreshList(), refreshInstance()]);
				}
			}
		}
	}

	function backUp() {
		void run('Backup', 'Backup created.', async () => {
			const { data, error: err, response } = await api.POST('/instances/{instanceId}/backups', {
				params: { path: { instanceId: id } }
			});
			return data ? { job: data } : { message: response.status === 401 ? '' : errorMessage(err) };
		});
	}

	function restore(b: Backup) {
		confirming = null;
		void run('Restore', `Restored the backup from ${new Date(b.createdAt).toLocaleString()}.`, async () => {
			const { data, error: err, response } = await api.POST(
				'/instances/{instanceId}/backups/{backupId}/restore',
				{ params: { path: { instanceId: id, backupId: b.id } } }
			);
			return data ? { job: data } : { message: response.status === 401 ? '' : errorMessage(err) };
		});
	}

	onMount(() => {
		void refreshInstance();
		void refreshList();
		const t = setInterval(() => {
			if (document.visibilityState === 'visible' && !working) void refreshInstance();
		}, POLL_MS);
		return () => {
			clearInterval(t);
			ac?.abort();
		};
	});
</script>

<svelte:head><title>Backups - {inst?.name ?? 'Instance'} - steamcmd-serve</title></svelte:head>

<p><a href={resolve('/instances/[id]', { id })}>&larr; Back to instance</a></p>

{#if notFound}
	<p class="alert" role="alert">Instance not found.</p>
{:else}
	<h1>Backups{inst ? `: ${inst.name}` : ''}</h1>

	{#if inst}
		<p>State: <StateBadge state={inst.state} /></p>
	{/if}
	{#if error}<p class="alert" role="alert">{error}</p>{/if}
	{#if jobDone}<p class="ok" role="status">{jobDone}</p>{/if}

	<div class="row bar">
		<button type="button" class="primary" disabled={working} onclick={backUp}>
			{working && jobLabel === 'Backup' ? 'Backing up…' : 'Back up now'}
		</button>
		{#if job}
			<span class="job" role="status">
				{jobLabel}: {job.status}{job.message ? ` - ${job.message}` : ''}
				<progress max="1" value={job.progress ?? 0} aria-label="{jobLabel} progress"></progress>
				{Math.round((job.progress ?? 0) * 100)}%
			</span>
		{/if}
	</div>

	{#if !restorable && inst}
		<p class="hint">Restore is available only while the server is stopped or in error.</p>
	{/if}

	{#if list === null}
		<p>Loading…</p>
	{:else if list.length === 0}
		<p class="hint">No backups yet.</p>
	{:else}
		<div class="scroll">
			<table>
				<thead>
					<tr><th>Created</th><th>Size</th><th>Reason</th><th><span class="sr">Actions</span></th></tr>
				</thead>
				<tbody>
					{#each list as b (b.id)}
						<tr>
							<td>{new Date(b.createdAt).toLocaleString()}</td>
							<td>{humanSize(b.sizeBytes)}</td>
							<td>{REASON_LABEL[b.reason] ?? b.reason}</td>
							<td>
								{#if confirming !== b.id}
									<button
										type="button"
										disabled={working || !restorable}
										onclick={() => (confirming = b.id)}
									>
										Restore
									</button>
								{/if}
							</td>
						</tr>
						{#if confirming === b.id}
							<tr class="confirm">
								<td colspan="4">
									<div role="alertdialog" aria-label="Confirm restore">
										<p>
											Restore the backup from <strong>{new Date(b.createdAt).toLocaleString()}</strong>?
											The server must be stopped. Current data will be backed up first (as a
											"Before restore" backup), then replaced by this backup.
										</p>
										<div class="row">
											<button
												type="button"
												class="danger"
												disabled={working || !restorable}
												onclick={() => restore(b)}
											>
												Confirm restore
											</button>
											<button type="button" onclick={() => (confirming = null)}>Cancel</button>
										</div>
									</div>
								</td>
							</tr>
						{/if}
					{/each}
				</tbody>
			</table>
		</div>
	{/if}
{/if}

<style>
	.hint {
		color: var(--muted);
	}
	.ok {
		color: var(--ok);
	}
	.bar {
		align-items: center;
		margin-bottom: 0.75rem;
	}
	.job {
		display: inline-flex;
		gap: 0.5rem;
		align-items: center;
	}
	.scroll {
		overflow-x: auto;
	}
	table {
		width: 100%;
		border-collapse: collapse;
	}
	th,
	td {
		text-align: left;
		padding: 0.5rem;
		border-bottom: 1px solid var(--border);
	}
	.confirm td {
		background: var(--busy-bg);
		color: var(--text);
	}
	.sr {
		position: absolute;
		width: 1px;
		height: 1px;
		overflow: hidden;
		clip-path: inset(50%);
	}
</style>
