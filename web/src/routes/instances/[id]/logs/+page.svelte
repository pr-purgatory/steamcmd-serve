<script lang="ts">
	import { page } from '$app/state';
	import { resolve } from '$app/paths';
	import { onMount, tick } from 'svelte';
	import { api, BASE_URL } from '#lib/api/client.ts';
	import { createMockEventSource } from '#lib/api/mock-logs.ts';
	import type { Instance, InstanceState } from '#lib/api/types.ts';
	import StateBadge from '#lib/StateBadge.svelte';
	import {
		LineBuffer,
		createLogStream,
		highlightSegments,
		isAtBottom,
		logsUrl,
		visibleLines,
		type ConnStatus,
		type LogEventSource
	} from '#lib/logs.ts';

	const useMock = import.meta.env.VITE_MOCK === '1';
	const FLUSH_MS = 100;

	const id = $derived(page.params.id ?? '');

	const buffer = new LineBuffer();
	let rev = $state(0); // bumped (batched) whenever the buffer changes
	let inst = $state<Instance | null>(null);
	let liveState = $state<InstanceState | null>(null);
	let conn = $state<ConnStatus>('connecting');
	let query = $state('');
	let autoscroll = $state(true);
	let paused = $state(false);
	let frozenId = $state(0);
	let notFound = $state(false);
	let scroller = $state<HTMLDivElement | null>(null);

	const shown = $derived.by(() => {
		void rev;
		return visibleLines(buffer.toArray(), { query, uptoId: paused ? frozenId : null });
	});
	const newLines = $derived.by(() => {
		void rev;
		return paused ? Math.max(0, buffer.lastId - frozenId) : 0;
	});
	const badgeState = $derived(liveState ?? inst?.state ?? null);

	let flushTimer: ReturnType<typeof setTimeout> | null = null;
	function scheduleFlush() {
		if (flushTimer) return;
		flushTimer = setTimeout(() => {
			flushTimer = null;
			rev++;
		}, FLUSH_MS);
	}

	function togglePause() {
		if (paused) {
			paused = false;
			rev++;
		} else {
			frozenId = buffer.lastId;
			paused = true;
		}
	}

	function clear() {
		buffer.clear();
		frozenId = buffer.lastId;
		rev++;
	}

	function onScroll() {
		if (!scroller) return;
		// Scrolling up turns auto-scroll off; reaching the bottom turns it back on.
		autoscroll = isAtBottom(scroller);
	}

	function jumpToBottom() {
		autoscroll = true;
	}

	$effect(() => {
		void shown;
		if (autoscroll && scroller) {
			const el = scroller;
			void tick().then(() => {
				el.scrollTop = el.scrollHeight;
			});
		}
	});

	const statusText: Record<ConnStatus, string> = {
		connecting: 'Connecting…',
		open: 'Live',
		reconnecting: 'Reconnecting…',
		closed: 'Disconnected'
	};

	onMount(() => {
		const instanceId = id;
		void api
			.GET('/instances/{instanceId}', { params: { path: { instanceId } } })
			.then(({ data, response }) => {
				if (data) inst = data;
				else if (response.status === 404) notFound = true;
			});

		const stream = createLogStream({
			url: (tail) => logsUrl(BASE_URL, instanceId, tail),
			make: (url) =>
				useMock ? createMockEventSource(url) : (new EventSource(url) as LogEventSource),
			onLine: (text) => {
				buffer.push(text);
				scheduleFlush();
			},
			onState: (s) => (liveState = s),
			onStatus: (s) => (conn = s)
		});
		return () => {
			stream.close();
			if (flushTimer) clearTimeout(flushTimer);
		};
	});
</script>

<svelte:head><title>Logs - {inst?.name ?? 'Instance'} - steamcmd-serve</title></svelte:head>

<p><a href={resolve('/instances/[id]', { id })}>&larr; Back to instance</a></p>

{#if notFound}
	<p class="alert" role="alert">Instance not found.</p>
{:else}
	<h1>Logs{inst ? `: ${inst.name}` : ''}</h1>

	<div class="row status">
		{#if badgeState}<StateBadge state={badgeState} />{/if}
		<span class="conn conn-{conn}" role="status">
			<span aria-hidden="true">{conn === 'open' ? '●' : conn === 'closed' ? '○' : '◌'}</span>
			{statusText[conn]}
		</span>
		<span class="hint">{shown.length} of {buffer.length} lines{query ? ' match' : ''}</span>
	</div>

	<div class="row controls">
		<label class="check">
			<input type="checkbox" bind:checked={autoscroll} />
			Auto-scroll
		</label>
		<button type="button" aria-pressed={paused} onclick={togglePause}>
			{paused ? 'Resume' : 'Pause'}
		</button>
		<button type="button" onclick={clear}>Clear</button>
		<label class="filter">
			<span class="sr">Filter lines</span>
			<input type="search" placeholder="Filter (case-insensitive)" bind:value={query} />
		</label>
	</div>

	{#if paused}
		<p class="paused" role="status">
			Paused. {newLines} new {newLines === 1 ? 'line' : 'lines'}.
			<button type="button" onclick={togglePause}>Resume</button>
		</p>
	{/if}

	<!-- svelte-ignore a11y_no_noninteractive_tabindex (keyboard-scrollable log region) -->
	<div
		class="log"
		bind:this={scroller}
		onscroll={onScroll}
		role="log"
		aria-label="Server log"
		tabindex="0"
	>
		{#each shown as line (line.id)}
			<div class="line">{#each highlightSegments(line.text, query) as seg, i (i)}{#if seg.match}<mark>{seg.text}</mark>{:else}{seg.text}{/if}{/each}</div>
		{:else}
			<div class="empty">{buffer.length ? 'No lines match the filter.' : 'Waiting for log output…'}</div>
		{/each}
	</div>
	{#if !autoscroll}
		<p><button type="button" onclick={jumpToBottom}>Jump to latest</button></p>
	{/if}
{/if}

<style>
	.status,
	.controls {
		align-items: center;
		margin-bottom: 0.75rem;
	}
	.hint {
		color: var(--muted);
	}
	.conn-open {
		color: var(--ok);
	}
	.conn-reconnecting,
	.conn-connecting {
		color: var(--busy);
	}
	.conn-closed {
		color: var(--err);
	}
	.check {
		display: inline-flex;
		gap: 0.4rem;
		align-items: center;
	}
	.filter {
		flex: 1 1 14rem;
	}
	.filter input {
		width: 100%;
	}
	.sr {
		position: absolute;
		width: 1px;
		height: 1px;
		overflow: hidden;
		clip-path: inset(50%);
	}
	.paused {
		background: var(--busy-bg);
		color: var(--busy);
		border-radius: var(--radius);
		padding: 0.4rem 0.75rem;
	}
	.log {
		font: 0.85rem/1.4 ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
		background: var(--surface);
		border: 1px solid var(--border);
		border-radius: var(--radius);
		height: 60vh;
		overflow: auto;
		padding: 0.5rem;
	}
	.line {
		white-space: pre-wrap;
		overflow-wrap: anywhere;
	}
	.empty {
		color: var(--muted);
	}
	mark {
		background: var(--busy-bg);
		color: var(--busy);
		outline: 1px solid var(--busy);
	}
</style>
