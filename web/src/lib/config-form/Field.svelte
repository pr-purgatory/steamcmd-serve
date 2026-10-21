<script lang="ts">
	import Field from './Field.svelte';
	import {
		generatePassword,
		isMinuteUnit,
		minToNs,
		newItem,
		niceStep,
		nsToMin,
		sameJson,
		type FieldErrors,
		type Json,
		type Schema
	} from './logic.ts';

	interface Props {
		schema: Schema;
		value: Json;
		/** JSON-pointer-style instance path, e.g. /userGroups/0/name ('' for the root). */
		path: string;
		name: string;
		errors: FieldErrors;
		/** Server value at this path; used to tell whether a stored secret exists. */
		original?: Json;
		disabled?: boolean;
		/** Reason shown when disabled. */
		reason?: string;
		onchange: (v: Json) => void;
	}

	let { schema, value, path, name, errors, original, disabled = false, reason, onchange }: Props =
		$props();

	const id = $derived(`f-${path.replace(/[^A-Za-z0-9]+/g, '-')}`);
	const label = $derived(schema.title ?? name);
	const err = $derived(errors[path]);
	const helpId = $derived(`${id}-help`);
	const errId = $derived(`${id}-err`);
	const describedBy = $derived(
		[schema.description ? helpId : '', err ? errId : ''].filter(Boolean).join(' ') || undefined
	);

	const kind = $derived.by(() => {
		if (schema['x-secret']) return 'secret';
		if (schema.enum) return 'enum';
		switch (schema.type) {
			case 'boolean':
				return 'boolean';
			case 'integer':
			case 'number':
				return isMinuteUnit(schema) ? 'minutes' : 'number';
			case 'string':
				return 'string';
			case 'array':
				if (schema.items?.type === 'object') return 'objects';
				if (schema.items?.type === 'string') return 'lines';
				return 'unsupported';
			case 'object':
				return schema.properties ? 'object' : 'unsupported';
			default:
				return 'unsupported';
		}
	});

	const canReset = $derived(
		schema.default !== undefined && !schema['x-secret'] && !sameJson(value, schema.default)
	);
	const hadSecret = $derived(
		typeof original === 'object' && original !== null && !Array.isArray(original)
			? (original as Record<string, Json>).hasValue === true
			: false
	);

	// Numbers: display value (minutes for ns fields).
	const integer = $derived(schema.type === 'integer');
	const isMin = $derived(kind === 'minutes');
	const toDisplay = (v: Json): number | null => (typeof v === 'number' ? (isMin ? nsToMin(v) : v) : null);
	const dMin = $derived(schema.minimum === undefined ? undefined : isMin ? nsToMin(schema.minimum) : schema.minimum);
	const dMax = $derived(schema.maximum === undefined ? undefined : isMin ? nsToMin(schema.maximum) : schema.maximum);
	const step = $derived(isMin ? 0.5 : niceStep(schema, integer));
	const hasRange = $derived(dMin !== undefined && dMax !== undefined);

	function setNumber(raw: string) {
		if (raw.trim() === '') return onchange(null);
		const n = Number(raw);
		if (!Number.isFinite(n)) return onchange(null);
		onchange(isMin ? minToNs(n) : n);
	}

	function setProp(key: string, v: Json) {
		onchange({ ...(value as Record<string, Json>), [key]: v });
	}
	function setItem(i: number, v: Json) {
		const arr = [...(value as Json[])];
		arr[i] = v;
		onchange(arr);
	}
	function addItem() {
		onchange([...((value as Json[]) ?? []), newItem(schema)]);
	}
	function removeItem(i: number) {
		onchange((value as Json[]).filter((_, j) => j !== i));
	}
	const origAt = (o: Json | undefined, k: string | number): Json | undefined =>
		o && typeof o === 'object' ? (o as Record<string, Json>)[k as string] : undefined;

	const lines = $derived(Array.isArray(value) ? value.join('\n') : '');
	function setLines(raw: string) {
		onchange(raw.split('\n').map((l) => l.trim()).filter((l) => l !== ''));
	}

	let showSecret = $state(false);
	function generate() {
		onchange(generatePassword(20));
		showSecret = true;
	}
	const minItems = $derived(schema.minItems ?? 0);
</script>

{#snippet help()}
	{#if schema.description}<p class="help" id={helpId}>{schema.description}</p>{/if}
	{#if err}<p class="field-err" id={errId} role="alert">{err}</p>{/if}
{/snippet}

{#snippet reset()}
	{#if canReset}
		<button type="button" class="link" {disabled} onclick={() => onchange(schema.default as Json)}>
			Reset to default
		</button>
	{/if}
{/snippet}

{#if kind === 'object'}
	<fieldset class="obj" {disabled} aria-describedby={describedBy} class:off={disabled}>
		<legend>{label}</legend>
		{#if disabled && reason}<p class="reason">Disabled: {reason}</p>{:else}{@render help()}{/if}
		{#if disabled && err}<p class="field-err" role="alert">{err}</p>{/if}
		<div class="reset">{@render reset()}</div>
		{#each Object.entries(schema.properties ?? {}) as [key, ps] (key)}
			<Field
				schema={ps}
				value={(value as Record<string, Json>)[key]}
				path={`${path}/${key}`}
				name={key}
				{errors}
				original={origAt(original, key)}
				{disabled}
				onchange={(v) => setProp(key, v)}
			/>
		{/each}
	</fieldset>
{:else if kind === 'objects'}
	<fieldset class="obj" {disabled} aria-describedby={describedBy}>
		<legend>{label}</legend>
		{@render help()}
		<div class="reset">{@render reset()}</div>
		{#each (value as Json[]) ?? [] as item, i (i)}
			<div class="item card">
				<div class="item-head">
					<strong>{schema.title ?? name} {i + 1}</strong>
					<button
						type="button"
						onclick={() => removeItem(i)}
						disabled={disabled || (value as Json[]).length <= minItems}
						aria-label={`Remove ${schema.title ?? name} ${i + 1}`}
					>
						Remove
					</button>
				</div>
				{#each Object.entries(schema.items?.properties ?? {}) as [key, ps] (key)}
					<Field
						schema={ps}
						value={(item as Record<string, Json>)[key]}
						path={`${path}/${i}/${key}`}
						name={key}
						{errors}
						original={origAt(origAt(original, i), key)}
						{disabled}
						onchange={(v) => setItem(i, { ...(item as Record<string, Json>), [key]: v })}
					/>
				{/each}
			</div>
		{/each}
		<button type="button" {disabled} onclick={addItem}>Add {schema.title ?? name}</button>
	</fieldset>
{:else}
	<div class="field" class:check={kind === 'boolean'}>
		{#if kind === 'boolean'}
			<label for={id}>
				<input
					{id}
					type="checkbox"
					{disabled}
					checked={value === true}
					aria-describedby={describedBy}
					aria-invalid={err ? 'true' : undefined}
					onchange={(e) => onchange(e.currentTarget.checked)}
				/>
				{label}
			</label>
		{:else}
			<label for={id}>{label}</label>
		{/if}

		{#if kind === 'enum'}
			<select
				{id}
				{disabled}
				aria-describedby={describedBy}
				aria-invalid={err ? 'true' : undefined}
				value={JSON.stringify(value)}
				onchange={(e) => onchange(JSON.parse(e.currentTarget.value) as Json)}
			>
				{#each schema.enum ?? [] as opt (JSON.stringify(opt))}
					<option value={JSON.stringify(opt)}>{String(opt)}</option>
				{/each}
			</select>
		{:else if kind === 'number' || kind === 'minutes'}
			{@const dv = toDisplay(value)}
			<div class="num">
				<input
					{id}
					type="number"
					{disabled}
					min={dMin}
					max={dMax}
					step={isMin ? step : integer ? 1 : 'any'}
					value={dv === null ? '' : Math.round(dv * 1e6) / 1e6}
					aria-describedby={describedBy}
					aria-invalid={err ? 'true' : undefined}
					oninput={(e) => setNumber(e.currentTarget.value)}
				/>
				{#if isMin}<span class="unit">min</span>{/if}
				{#if hasRange}
					<input
						type="range"
						{disabled}
						min={dMin}
						max={dMax}
						{step}
						value={dv ?? dMin}
						aria-label={`${label} slider`}
						oninput={(e) => setNumber(e.currentTarget.value)}
					/>
				{/if}
			</div>
		{:else if kind === 'string'}
			<input
				{id}
				type="text"
				{disabled}
				minlength={schema.minLength}
				maxlength={schema.maxLength}
				pattern={schema.pattern}
				value={typeof value === 'string' ? value : ''}
				aria-describedby={describedBy}
				aria-invalid={err ? 'true' : undefined}
				oninput={(e) => onchange(e.currentTarget.value)}
			/>
		{:else if kind === 'secret'}
			<div class="num">
				<input
					{id}
					type={showSecret ? 'text' : 'password'}
					autocomplete="new-password"
					{disabled}
					value={typeof value === 'string' ? value : ''}
					placeholder={hadSecret && value === '' ? 'unchanged' : ''}
					aria-describedby={describedBy}
					aria-invalid={err ? 'true' : undefined}
					oninput={(e) => onchange(e.currentTarget.value)}
				/>
				<button type="button" {disabled} onclick={() => (showSecret = !showSecret)}>
					{showSecret ? 'Hide' : 'Show'}
				</button>
				{#if schema['x-generate'] === 'password'}
					<button type="button" {disabled} onclick={generate}>Generate</button>
				{/if}
			</div>
		{:else if kind === 'lines'}
			<textarea
				{id}
				{disabled}
				rows="3"
				value={lines}
				aria-describedby={describedBy}
				aria-invalid={err ? 'true' : undefined}
				onchange={(e) => setLines(e.currentTarget.value)}
			></textarea>
			<span class="hint">One per line</span>
		{:else}
			<p class="field-err">Unsupported field type.</p>
		{/if}

		{@render help()}
		{@render reset()}
	</div>
{/if}

<style>
	.field {
		margin: 0 0 1rem;
	}
	.field label {
		display: block;
		font-weight: 600;
		margin-bottom: 0.25rem;
	}
	.field.check label {
		display: flex;
		gap: 0.5rem;
		align-items: center;
	}
	.field.check input {
		width: auto;
	}
	.num {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
		align-items: center;
	}
	.num input[type='number'] {
		width: 9rem;
	}
	.num input[type='range'] {
		flex: 1 1 10rem;
		width: auto;
	}
	.help,
	.hint,
	.reason {
		color: var(--muted);
		font-size: 0.875rem;
		margin: 0.25rem 0;
	}
	.field-err {
		color: var(--err);
		font-size: 0.875rem;
		margin: 0.25rem 0;
	}
	.link {
		border: 0;
		background: none;
		color: var(--accent);
		padding: 0;
		text-decoration: underline;
		font-size: 0.875rem;
		cursor: pointer;
	}
	fieldset.obj {
		border: 1px solid var(--border);
		border-radius: var(--radius);
		margin: 0 0 1rem;
		padding: 0.75rem 1rem;
	}
	fieldset.off {
		opacity: 0.7;
	}
	legend {
		font-weight: 600;
		padding: 0 0.4rem;
	}
	.reset {
		margin-bottom: 0.5rem;
	}
	.item {
		margin-bottom: 0.75rem;
	}
	.item-head {
		display: flex;
		justify-content: space-between;
		align-items: center;
		margin-bottom: 0.5rem;
	}
</style>
