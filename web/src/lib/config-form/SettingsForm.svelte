<script lang="ts">
	import Field from './Field.svelte';
	import {
		groupLabel,
		groupProperties,
		isEnabled,
		type FieldErrors,
		type Json,
		type Schema
	} from './logic.ts';

	interface Props {
		schema: Schema;
		value: Record<string, Json>;
		original: Json;
		errors: FieldErrors;
		onchange: (v: Record<string, Json>) => void;
	}
	let { schema, value, original, errors, onchange }: Props = $props();

	const groups = $derived(groupProperties(schema));
	const orig = $derived((original ?? {}) as Record<string, Json>);

	/** Why a property is disabled: the property's own description. */
	function reasonFor(ps: Schema): string {
		return ps.description ?? 'Not editable with the current settings';
	}
</script>

{#each groups as g (g.name)}
	<section class="card" aria-labelledby={`grp-${g.name}`}>
		<h2 id={`grp-${g.name}`}>{groupLabel(g.name)}</h2>
		{#each g.keys as key (key)}
			{@const ps = (schema.properties as Record<string, Schema>)[key]}
			{@const enabled = isEnabled(ps, value)}
			<Field
				schema={ps}
				value={value[key]}
				path={`/${key}`}
				name={key}
				{errors}
				original={orig[key]}
				disabled={!enabled}
				reason={enabled ? undefined : reasonFor(ps)}
				onchange={(v) => onchange({ ...value, [key]: v })}
			/>
		{/each}
	</section>
{/each}
