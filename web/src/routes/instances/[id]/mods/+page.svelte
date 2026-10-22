<script lang="ts">
	import { page } from '$app/state';
	import { resolve } from '$app/paths';
	import { onMount } from 'svelte';
	import { api, errorMessage } from '#lib/api/client.ts';
	import type { Game, InstalledMod, Instance, Job, ModPackage, ModProvider } from '#lib/api/types.ts';
	import StateBadge from '#lib/StateBadge.svelte';
	import { STATE_LABEL } from '#lib/states.ts';
	import { humanSize } from '#lib/backups.ts';
	import { JobFailedError, isAbortError, pollJob } from '#lib/jobs.ts';
	import {
		buildUploadForm,
		canChangeMods,
		debounce,
		findInstalled,
		isManualDownloadFailure,
		normalizeQuery,
		requiredByLabel,
		uploadProblem
	} from '#lib/mods.ts';

	const POLL_MS = 5000;
	const id = $derived(page.params.id ?? '');

	const PROVIDER_LABEL: Record<ModProvider, string> = { thunderstore: 'Thunderstore', nexus: 'NexusMods' };

	let inst = $state<Instance | null>(null);
	let game = $state<Game | null>(null);
	let notFound = $state(false);
	let installed = $state<InstalledMod[] | null>(null);
	let provider = $state<ModProvider>('thunderstore');
	let query = $state('');
	let results = $state<ModPackage[] | null>(null);
	let searching = $state(false);
	let searchError = $state<string | null>(null);

	let error = $state<string | null>(null);
	let notice = $state<string | null>(null);
	let jobDone = $state<string | null>(null);
	let job = $state<Job | null>(null);
	let jobLabel = $state('');
	let working = $state(false);
	let confirming = $state<string | null>(null);

	let manual = $state<{ provider: ModProvider; pkg: ModPackage } | null>(null);
	let file = $state<File | null>(null);
	let fileInput = $state<HTMLInputElement | null>(null);

	let ac: AbortController | null = null;
	let searchSeq = 0;

	const providers = $derived<ModProvider[]>(game?.modProviders ?? []);
	const unsupported = $derived(game !== null && game.modLoader === 'none');
	const mutable = $derived(inst ? canChangeMods(inst.state) : false);
	const locked = $derived(working || !mutable);
	const fileProblem = $derived(file ? uploadProblem(file) : null);

	async function refreshInstance() {
		const { data, response } = await api.GET('/instances/{instanceId}', {
			params: { path: { instanceId: id } }
		});
		if (data) {
			inst = data;
			if (!game || game.id !== data.gameId) {
				const g = await api.GET('/games');
				game = g.data?.find((x) => x.id === data.gameId) ?? null;
				if (game?.modProviders?.length && !game.modProviders.includes(provider)) {
					provider = game.modProviders[0];
				}
			}
		} else if (response.status === 404) notFound = true;
	}

	async function refreshInstalled() {
		const { data, error: err, response } = await api.GET('/instances/{instanceId}/mods', {
			params: { path: { instanceId: id } }
		});
		if (data) installed = data;
		else if (response.status !== 401) error = errorMessage(err, 'Could not load installed mods');
	}

	async function search() {
		const q = normalizeQuery(query);
		if (q === null || !game || unsupported) {
			results = null;
			searchError = null;
			return;
		}
		const seq = ++searchSeq;
		searching = true;
		searchError = null;
		const { data, error: err, response } = await api.GET('/games/{gameId}/mods', {
			params: { path: { gameId: game.id }, query: { provider, q } }
		});
		if (seq !== searchSeq) return;
		searching = false;
		if (data) results = data;
		else if (response.status !== 401) {
			results = [];
			searchError = errorMessage(err, 'Search failed');
		}
	}
	const debouncedSearch = debounce(() => void search(), 400);

	function selectProvider(p: ModProvider) {
		if (p === provider) return;
		provider = p;
		results = null;
		debouncedSearch.cancel();
		void search();
	}

	/** Runs a request that returns a job, polls it, then refreshes. */
	async function run(
		label: string,
		start: () => Promise<{ job?: Job; message?: string }>,
		onJobFailed?: (e: JobFailedError) => boolean
	) {
		if (working) return;
		working = true;
		error = null;
		notice = null;
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
			const final = await pollJob(
				async () => {
					const { data, error: err } = await api.GET('/jobs/{jobId}', { params: { path: { jobId } } });
					if (!data) throw new Error(errorMessage(err, 'Could not read job status'));
					return data;
				},
				{ intervalMs: 1000, signal: mine.signal, onUpdate: (j) => (job = j) }
			);
			jobDone = `${label}: ${final.message || 'done'}.`;
			manual = null;
			file = null;
		} catch (e) {
			if (e instanceof JobFailedError && onJobFailed?.(e)) {
				/* handled by caller */
			} else if (!isAbortError(e)) error = `${label}: ${e instanceof Error ? e.message : String(e)}`;
		} finally {
			if (ac === mine) {
				working = false;
				job = null;
				if (!mine.signal.aborted) await Promise.all([refreshInstalled(), refreshInstance()]);
			}
		}
	}

	function install(pkg: ModPackage) {
		const prov = provider;
		manual = null;
		file = null;
		void run(
			`Install ${pkg.name}`,
			async () => {
				const { data, error: err, response } = await api.POST('/instances/{instanceId}/mods', {
					params: { path: { instanceId: id } },
					body: { provider: prov, ref: { id: pkg.ref.id, version: pkg.ref.version } }
				});
				return data ? { job: data } : { message: response.status === 401 ? '' : errorMessage(err) };
			},
			(e) => {
				if (prov === 'nexus' && isManualDownloadFailure(e.message)) {
					manual = { provider: prov, pkg };
					notice = e.message;
					return true;
				}
				return false;
			}
		);
	}

	function upload() {
		const target = manual;
		if (!target || !file || uploadProblem(file)) return;
		const f = file;
		void run(`Upload ${target.pkg.name}`, async () => {
			const form = buildUploadForm(target.provider, target.pkg.ref, f, f.name);
			const { data, error: err, response } = await api.POST('/instances/{instanceId}/mods', {
				params: { path: { instanceId: id } },
				// eslint-disable-next-line @typescript-eslint/no-explicit-any
				body: undefined as any,
				bodySerializer: () => form
			});
			return data ? { job: data } : { message: response.status === 401 ? '' : errorMessage(err) };
		});
	}

	function uninstall(m: InstalledMod) {
		confirming = null;
		void run(`Uninstall ${m.ref.id}`, async () => {
			const { data, error: err, response } = await api.DELETE('/instances/{instanceId}/mods/{modId}', {
				params: { path: { instanceId: id, modId: m.id } }
			});
			return data ? { job: data } : { message: response.status === 401 ? '' : errorMessage(err) };
		});
	}

	function onFile(e: Event) {
		file = (e.currentTarget as HTMLInputElement).files?.[0] ?? null;
	}

	function cancelManual() {
		manual = null;
		file = null;
		if (fileInput) fileInput.value = '';
	}

	onMount(() => {
		void refreshInstance().then(() => {
			if (!unsupported) {
				void refreshInstalled();
				void search();
			}
		});
		const t = setInterval(() => {
			if (document.visibilityState === 'visible' && !working) void refreshInstance();
		}, POLL_MS);
		return () => {
			clearInterval(t);
			debouncedSearch.cancel();
			ac?.abort();
		};
	});
</script>

<svelte:head><title>Mods - {inst?.name ?? 'Instance'} - steamcmd-serve</title></svelte:head>

<p><a href={resolve('/instances/[id]', { id })}>&larr; Back to instance</a></p>

{#if notFound}
	<p class="alert" role="alert">Instance not found.</p>
{:else if !inst || !game}
	<p>Loading…</p>
{:else if unsupported}
	<h1>Mods: {inst.name}</h1>
	<p class="card">{game.name} does not support mods, so there is nothing to manage here.</p>
{:else}
	<h1>Mods: {inst.name}</h1>
	<p>State: <StateBadge state={inst.state} /></p>
	{#if !mutable}
		<p class="hint" role="status">
			Mods can only be changed while the server is stopped or in error (currently {STATE_LABEL[inst.state]}).
			Install and uninstall are disabled.
		</p>
	{/if}
	{#if error}<p class="alert" role="alert">{error}</p>{/if}
	{#if jobDone}<p class="ok" role="status">{jobDone}</p>{/if}
	{#if job}
		<p class="job" role="status">
			{jobLabel}: {job.status}{job.message ? ` - ${job.message}` : ''}
			<progress max="1" value={job.progress ?? 0} aria-label="{jobLabel} progress"></progress>
			{Math.round((job.progress ?? 0) * 100)}%
		</p>
	{/if}

	{#if manual}
		<section class="card" aria-labelledby="manual-h">
			<h2 id="manual-h">Upload the file you downloaded from NexusMods</h2>
			<p>
				{notice ?? 'NexusMods needs a manual download for this mod.'}
			</p>
			<p>
				Download <strong>{manual.pkg.name}</strong> (version {manual.pkg.ref.version}) from nexusmods.com,
				then upload the .zip here. Uploaded archives are not dependency-resolved: install any
				required mods yourself.
			</p>
			<div class="row">
				<label>
					Mod archive (.zip, up to 1 GiB)
					<input
						type="file"
						accept=".zip"
						bind:this={fileInput}
						onchange={onFile}
						disabled={locked}
					/>
				</label>
			</div>
			{#if file}
				<p>
					<code>{file.name}</code> ({humanSize(file.size)})
				</p>
			{/if}
			{#if fileProblem}<p class="alert" role="alert">{fileProblem}</p>{/if}
			<div class="row">
				<button type="button" class="primary" disabled={locked || !file || !!fileProblem} onclick={upload}>
					{working ? 'Uploading…' : 'Upload and install'}
				</button>
				<button type="button" disabled={working} onclick={cancelManual}>Cancel</button>
			</div>
		</section>
	{/if}

	<section class="card" aria-labelledby="installed-h">
		<h2 id="installed-h">Installed mods</h2>
		{#if installed === null}
			<p>Loading…</p>
		{:else if installed.length === 0}
			<p class="hint">No mods installed.</p>
		{:else}
			<ul class="list">
				{#each installed as m (m.provider + ':' + m.id)}
					<li>
						<div class="line">
							<span class="badge">{PROVIDER_LABEL[m.provider]}</span>
							<strong>{m.ref.id}</strong>
							<span class="hint">v{m.ref.version}</span>
							{#if !m.explicit}<span class="hint">{requiredByLabel(m, installed)}</span>{/if}
							{#if m.explicit && confirming !== m.id}
								<button type="button" disabled={locked} onclick={() => (confirming = m.id)}>
									Uninstall
								</button>
							{/if}
						</div>
						{#if confirming === m.id}
							<div role="alertdialog" aria-label="Confirm uninstall" class="confirm">
								<p>
									Uninstall <strong>{m.ref.id}</strong>? Dependencies no other mod needs are removed too.
								</p>
								<div class="row">
									<button type="button" class="danger" disabled={locked} onclick={() => uninstall(m)}>
										Confirm uninstall
									</button>
									<button type="button" onclick={() => (confirming = null)}>Cancel</button>
								</div>
							</div>
						{/if}
					</li>
				{/each}
			</ul>
		{/if}
	</section>

	<section class="card" aria-labelledby="find-h">
		<h2 id="find-h">Find mods</h2>
		{#if providers.length > 1}
			<div class="row tabs" role="group" aria-label="Mod provider">
				{#each providers as p (p)}
					<button
						type="button"
						class:active={p === provider}
						aria-pressed={p === provider}
						onclick={() => selectProvider(p)}
					>
						{PROVIDER_LABEL[p]}
					</button>
				{/each}
			</div>
		{:else if providers.length === 1}
			<p><span class="badge">{PROVIDER_LABEL[providers[0]]}</span></p>
		{/if}
		{#if provider === 'thunderstore'}
			<p class="hint">Dependencies are installed automatically.</p>
		{/if}
		<label>
			Search {PROVIDER_LABEL[provider]}
			<input
				type="search"
				bind:value={query}
				oninput={() => debouncedSearch()}
				placeholder="Type at least 2 characters (empty shows the top list)"
				maxlength="100"
			/>
		</label>
		{#if normalizeQuery(query) === null}
			<p class="hint">Type at least 2 characters to search.</p>
		{/if}
		{#if searchError}
			<p class="alert" role="alert">
				{searchError}
				{#if provider === 'nexus'}
					<a href={resolve('/settings')}>Open Settings</a>
				{/if}
			</p>
		{/if}
		{#if searching}<p class="hint" role="status">Searching…</p>{/if}

		{#if results && results.length === 0 && !searchError && !searching}
			<p class="hint">No mods found.</p>
		{/if}
		{#if results && results.length}
			<ul class="list">
				{#each results as pkg (pkg.ref.id)}
					{@const have = installed ? findInstalled(installed, provider, pkg.ref.id) : undefined}
					<li>
						<div class="line">
							<strong>{pkg.name}</strong>
							<span class="hint">v{pkg.ref.version}{pkg.author ? ` by ${pkg.author}` : ''}</span>
							{#if (pkg.dependencies?.length ?? 0) > 0}
								<span class="hint">{pkg.dependencies?.length} dependenc{pkg.dependencies?.length === 1 ? 'y' : 'ies'}</span>
							{/if}
							{#if have}
								<span class="badge ok-badge">Installed</span>
							{:else}
								<button type="button" disabled={locked} onclick={() => install(pkg)}>Install</button>
							{/if}
						</div>
						{#if pkg.summary}<p class="summary">{pkg.summary}</p>{/if}
					</li>
				{/each}
			</ul>
		{/if}
	</section>
{/if}

<style>
	.hint {
		color: var(--muted);
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
	.list {
		list-style: none;
		padding: 0;
		margin: 0;
	}
	.list li {
		padding: 0.5rem 0;
		border-bottom: 1px solid var(--border);
	}
	.line {
		display: flex;
		flex-wrap: wrap;
		gap: 0.4rem 0.75rem;
		align-items: center;
	}
	.summary {
		margin: 0.25rem 0 0;
		color: var(--muted);
	}
	.badge {
		border: 1px solid var(--border);
		border-radius: var(--radius);
		padding: 0 0.4rem;
		font-size: 0.85em;
	}
	.ok-badge {
		color: var(--ok);
	}
	.tabs button.active {
		font-weight: 700;
		border-color: var(--accent);
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
