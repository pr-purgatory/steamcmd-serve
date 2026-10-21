<script lang="ts">
	import type { InstanceState } from '#lib/api/types.ts';
	import { STATE_CLASS, STATE_GLYPH, STATE_LABEL } from '#lib/states.ts';

	let { state }: { state: InstanceState } = $props();
</script>

<span class="badge {STATE_CLASS[state]}">
	<span aria-hidden="true">{STATE_GLYPH[state]}</span>
	{STATE_LABEL[state]}
</span>
