import { STATE_LABEL } from './states.ts';
import type { InstanceState } from './api/types';

/** Maximum number of lines kept in memory (and therefore the DOM never exceeds this). */
export const LOG_CAP = 5000;
/** Initial history requested from the server. */
export const INITIAL_TAIL = 500;
/** Most lines rendered at once after filtering. */
export const RENDER_MAX = 2000;

export interface LogLine {
	/** Monotonic sequence number, starting at 1; stable across drops, usable as a key. */
	id: number;
	text: string;
}

/** Capped buffer: keeps the newest `cap` lines in arrival order, dropping the oldest. */
export class LineBuffer {
	readonly cap: number;
	#lines: LogLine[] = [];
	#seq = 0;

	constructor(cap = LOG_CAP) {
		this.cap = Math.max(1, Math.floor(cap));
	}

	push(text: string): void {
		this.#lines.push({ id: ++this.#seq, text });
		const extra = this.#lines.length - this.cap;
		if (extra > 0) this.#lines.splice(0, extra);
	}

	/** Id of the newest line ever pushed (0 when none). Not reset by clear(). */
	get lastId(): number {
		return this.#seq;
	}

	get length(): number {
		return this.#lines.length;
	}

	clear(): void {
		this.#lines = [];
	}

	toArray(): LogLine[] {
		return this.#lines.slice();
	}
}

/** Case-insensitive substring match; an empty query matches everything. */
export function matchesFilter(text: string, query: string): boolean {
	return query === '' || text.toLowerCase().includes(query.toLowerCase());
}

/**
 * Lines to show: filtered, optionally only those up to `uptoId` (used while paused),
 * limited to the newest `max`.
 */
export function visibleLines(
	lines: LogLine[],
	opts: { query?: string; uptoId?: number | null; max?: number } = {}
): LogLine[] {
	const { query = '', uptoId = null, max = RENDER_MAX } = opts;
	const out = lines.filter((l) => (uptoId === null || l.id <= uptoId) && matchesFilter(l.text, query));
	return out.length > max ? out.slice(out.length - max) : out;
}

export interface Segment {
	text: string;
	match: boolean;
}

/** Split text into plain / matching segments for highlighting (case-insensitive). */
export function highlightSegments(text: string, query: string): Segment[] {
	if (query === '') return [{ text, match: false }];
	const hay = text.toLowerCase();
	const needle = query.toLowerCase();
	// toLowerCase can change length for exotic characters; fall back to no highlight then.
	if (hay.length !== text.length) return [{ text, match: false }];
	const out: Segment[] = [];
	let pos = 0;
	for (;;) {
		const i = hay.indexOf(needle, pos);
		if (i < 0) break;
		if (i > pos) out.push({ text: text.slice(pos, i), match: false });
		out.push({ text: text.slice(i, i + needle.length), match: true });
		pos = i + needle.length;
	}
	if (pos < text.length) out.push({ text: text.slice(pos), match: false });
	return out.length ? out : [{ text, match: false }];
}

/** True when the scroll position is within `slack` px of the bottom. */
export function isAtBottom(
	el: { scrollHeight: number; scrollTop: number; clientHeight: number },
	slack = 8
): boolean {
	return el.scrollHeight - el.scrollTop - el.clientHeight <= slack;
}

/** History to request: the first connection asks for a tail, every reconnect asks for none. */
export function logsTail(hasConnected: boolean, initialTail = INITIAL_TAIL): number {
	return hasConnected ? 0 : initialTail;
}

export function logsUrl(base: string, instanceId: string, tail: number): string {
	return `${base}/instances/${encodeURIComponent(instanceId)}/logs?tail=${tail}`;
}

export function parseState(data: string): InstanceState | null {
	const s = data.trim().replace(/^"|"$/g, '');
	return s in STATE_LABEL ? (s as InstanceState) : null;
}

/** The subset of EventSource used here (also implemented by the dev-only mock). */
export interface LogEventSource {
	addEventListener(type: string, listener: (e: Event) => void): void;
	close(): void;
	onopen: ((e: Event) => unknown) | null;
	onerror: ((e: Event) => unknown) | null;
}

export type ConnStatus = 'connecting' | 'open' | 'reconnecting' | 'closed';

export interface LogStreamOptions {
	url: (tail: number) => string;
	make: (url: string) => LogEventSource;
	onLine: (text: string) => void;
	onState: (s: InstanceState) => void;
	onStatus: (s: ConnStatus) => void;
	initialTail?: number;
	reconnectMs?: number;
	maxReconnectMs?: number;
}

/**
 * Manages the EventSource ourselves instead of relying on its built-in reconnect: after the
 * first successful connection every reopen uses tail=0, so history is never sent twice.
 */
export function createLogStream(o: LogStreamOptions): { close(): void } {
	const base = o.reconnectMs ?? 1000;
	const max = o.maxReconnectMs ?? 10000;
	let connected = false;
	let closed = false;
	let attempt = 0;
	let es: LogEventSource | null = null;
	let timer: ReturnType<typeof setTimeout> | null = null;

	function open() {
		timer = null;
		if (closed) return;
		o.onStatus(connected ? 'reconnecting' : 'connecting');
		const cur = o.make(o.url(logsTail(connected, o.initialTail)));
		es = cur;
		cur.onopen = () => {
			if (es !== cur) return;
			connected = true;
			attempt = 0;
			o.onStatus('open');
		};
		cur.addEventListener('log', (e) => {
			if (es === cur) o.onLine(String((e as MessageEvent).data));
		});
		cur.addEventListener('state', (e) => {
			if (es !== cur) return;
			const s = parseState(String((e as MessageEvent).data));
			if (s) o.onState(s);
		});
		cur.onerror = () => {
			if (es !== cur) return;
			cur.close();
			es = null;
			if (closed) return;
			o.onStatus('reconnecting');
			const delay = Math.min(max, base * 2 ** attempt);
			attempt++;
			timer = setTimeout(open, delay);
		};
	}

	open();

	return {
		close() {
			closed = true;
			if (timer) clearTimeout(timer);
			timer = null;
			const cur = es;
			es = null;
			cur?.close();
			o.onStatus('closed');
		}
	};
}
