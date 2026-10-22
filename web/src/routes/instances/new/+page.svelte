<script lang="ts">
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import { onMount } from 'svelte';
	import { api, errorMessage } from '#lib/api/client.ts';
	import type { Game } from '#lib/api/types.ts';
	import { setCreatedWarnings } from '#lib/created.ts';
	import {
		NAME_MAX,
		createPortsPayload,
		defaultInputs,
		resolvePorts,
		validateMemory,
		validateName,
		validatePorts
	} from '#lib/ports.ts';

	let games = $state<Game[] | null>(null);
	let loadError = $state<string | null>(null);

	let step = $state(1);
	let gameId = $state('');
	let name = $state('');
	let portInputs = $state<Record<string, string>>({});
	let memory = $state('');

	let submitError = $state<string | null>(null);
	let busy = $state(false);
	let touchedNext = $state(false);

	const game = $derived(games?.find((g) => g.id === gameId));
	const nameError = $derived(validateName(name));
	const rows = $derived(game ? resolvePorts(game.ports, portInputs) : []);
	const portErrors = $derived(game ? validatePorts(game.ports, portInputs) : {});
	const memoryError = $derived(validateMemory(memory));
	const portsValid = $derived(Object.keys(portErrors).length === 0 && memoryError === null);

	onMount(() => {
		void api.GET('/games').then(({ data, error, response }) => {
			if (data) games = data;
			else if (response.status !== 401) loadError = errorMessage(error, 'Could not load games');
		});
	});

	function pickGame(id: string) {
		gameId = id;
		const g = games?.find((x) => x.id === id);
		if (g) {
			portInputs = defaultInputs(g.ports);
			memory = String(g.resources.memoryMB);
		}
		submitError = null;
	}

	function next() {
		touchedNext = true;
		if (step === 1 && !game) return;
		if (step === 2 && nameError) return;
		touchedNext = false;
		step += 1;
	}

	function back() {
		submitError = null;
		touchedNext = false;
		step -= 1;
	}

	async function submit(e: SubmitEvent) {
		e.preventDefault();
		if (step < 3) {
			next();
			return;
		}
		touchedNext = true;
		// Hard guard: never submit invalid values.
		if (!game || nameError || !portsValid) return;
		submitError = null;
		busy = true;
		const { response, error, data } = await api.POST('/instances', {
			body: {
				name,
				gameId: game.id,
				ports: createPortsPayload(game.ports, portInputs),
				memoryMB: Number(memory)
			}
		});
		busy = false;
		if (response.ok && data) {
			const warnings = data.warnings ?? [];
			setCreatedWarnings(data.id, warnings);
			await goto(`${resolve('/instances/[id]', { id: data.id })}?created=1`);
		} else if (response.status !== 401) {
			submitError = errorMessage(error, `Create failed (${response.status})`);
		}
	}
</script>

<svelte:head><title>New instance - steamcmd-serve</title></svelte:head>

<h1>New instance</h1>

<ol class="steps" aria-label="Progress">
	{#each ['Game', 'Name', 'Ports and memory'] as label, i (label)}
		<li aria-current={step === i + 1 ? 'step' : undefined} class:done={step > i + 1}>
			{i + 1}. {label}
		</li>
	{/each}
</ol>

{#if loadError}<p class="alert" role="alert">{loadError}</p>{/if}
{#if submitError}<p class="alert" role="alert">{submitError}</p>{/if}

{#if games === null && !loadError}
	<p>Loading games…</p>
{:else if games}
	<form class="card" onsubmit={submit} novalidate>
		{#if step === 1}
			<fieldset>
				<legend><h2>Pick a game</h2></legend>
				<div class="games">
					{#each games as g (g.id)}
						<label class="game" class:selected={gameId === g.id} for="game-{g.id}">
							<input
								id="game-{g.id}"
								aria-label={g.name}
								type="radio"
								name="game"
								value={g.id}
								checked={gameId === g.id}
								onchange={() => pickGame(g.id)}
							/>
							<span class="gname">{g.name}</span>
							<span class="hint">
								{g.platform}, {g.resources.memoryMB} MB RAM, {g.resources.diskGB} GB disk
							</span>
						</label>
					{/each}
				</div>
				{#if touchedNext && !game}<p class="field-error">Choose a game to continue.</p>{/if}
			</fieldset>
		{:else if step === 2}
			<h2>Name your instance</h2>
			<div class="field">
				<label for="name">Instance name</label>
				<!-- svelte-ignore a11y_autofocus -->
				<input
					id="name"
					bind:value={name}
					maxlength={NAME_MAX}
					autocomplete="off"
					autofocus
					aria-invalid={touchedNext && nameError ? 'true' : undefined}
					aria-describedby="name-hint{touchedNext && nameError ? ' name-err' : ''}"
				/>
				<p class="hint" id="name-hint">
					Up to {NAME_MAX} characters: letters, digits, space, underscore, dot, hyphen.
				</p>
				{#if touchedNext && nameError}<p class="field-error" id="name-err">{nameError}</p>{/if}
			</div>
		{:else if game}
			<h2>Ports and memory</h2>
			<p class="info">
				{game.name} needs about <strong>{game.resources.memoryMB} MB RAM</strong> and
				<strong>{game.resources.diskGB} GB disk</strong>.
			</p>

			{#each rows as row (row.name)}
				{@const err = portErrors[row.name]}
				<div class="field">
					<label for="port-{row.name}">
						{row.name} port ({row.protocol}){row.derived ? ' - derived' : ''}
					</label>
					{#if row.derived}
						<input
							id="port-{row.name}"
							value={row.port === null ? '' : String(row.port)}
							readonly
							aria-describedby="port-{row.name}-hint{err ? ` port-${row.name}-err` : ''}"
							aria-invalid={err ? 'true' : undefined}
						/>
						<p class="hint" id="port-{row.name}-hint">
							{row.derivedFrom} + {row.offset}{row.port === null ? '' : ` = ${row.port}/${row.protocol}`}.
							Read-only.
						</p>
					{:else}
						<input
							id="port-{row.name}"
							inputmode="numeric"
							autocomplete="off"
							bind:value={portInputs[row.name]}
							aria-invalid={err ? 'true' : undefined}
							aria-describedby="port-{row.name}-hint{err ? ` port-${row.name}-err` : ''}"
						/>
						<p class="hint" id="port-{row.name}-hint">
							{row.description ?? ''} 1024-65535.
						</p>
					{/if}
					{#if err}<p class="field-error" id="port-{row.name}-err">{err}</p>{/if}
				</div>
			{/each}

			<div class="field">
				<label for="memory">Memory limit (MB)</label>
				<input
					id="memory"
					inputmode="numeric"
					bind:value={memory}
					aria-invalid={memoryError ? 'true' : undefined}
					aria-describedby={memoryError ? 'mem-err' : undefined}
				/>
				{#if memoryError}<p class="field-error" id="mem-err">{memoryError}</p>{/if}
			</div>
		{/if}

		<div class="row actions">
			{#if step > 1}<button type="button" onclick={back}>Back</button>{/if}
			{#if step < 3}
				<button type="submit" class="primary" disabled={step === 1 ? !game : false}>Next</button>
			{:else}
				<button type="submit" class="primary" disabled={busy || !portsValid || !!nameError}>
					{busy ? 'Creating…' : 'Create instance'}
				</button>
			{/if}
			<a href={resolve('/')}>Cancel</a>
		</div>
	</form>
{/if}

<style>
	.steps {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem 1.25rem;
		list-style: none;
		padding: 0;
		margin: 0 0 1rem;
		color: var(--muted);
	}
	.steps li[aria-current='step'] {
		color: var(--text);
		font-weight: 700;
		border-bottom: 2px solid var(--accent);
	}
	.steps li.done {
		color: var(--ok);
	}
	fieldset {
		border: 0;
		padding: 0;
		margin: 0;
	}
	legend h2 {
		margin: 0 0 0.75rem;
	}
	.games {
		display: grid;
		gap: 0.75rem;
		grid-template-columns: repeat(auto-fit, minmax(14rem, 1fr));
	}
	.game {
		display: grid;
		gap: 0.15rem;
		border: 1px solid var(--border);
		border-radius: var(--radius);
		padding: 0.75rem;
		cursor: pointer;
		font-weight: 400;
		margin: 0;
	}
	.game input {
		width: auto;
		justify-self: start;
	}
	.game.selected {
		border: 2px solid var(--accent);
	}
	.game:has(input:focus-visible) {
		outline: 3px solid var(--focus);
		outline-offset: 2px;
	}
	.gname {
		font-weight: 700;
	}
	.info {
		background: var(--new-bg);
		color: var(--new);
		padding: 0.5rem 0.75rem;
		border-radius: var(--radius);
	}
	.actions {
		margin-top: 1rem;
	}
</style>
