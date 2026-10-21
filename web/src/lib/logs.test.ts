import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
	LineBuffer,
	createLogStream,
	highlightSegments,
	isAtBottom,
	logsTail,
	logsUrl,
	matchesFilter,
	visibleLines,
	type LogEventSource
} from './logs.ts';

describe('LineBuffer', () => {
	it('keeps order and drops the oldest beyond the cap', () => {
		const b = new LineBuffer(5);
		for (let i = 1; i <= 8; i++) b.push(`l${i}`);
		expect(b.length).toBe(5);
		expect(b.toArray().map((l) => l.text)).toEqual(['l4', 'l5', 'l6', 'l7', 'l8']);
		expect(b.toArray().map((l) => l.id)).toEqual([4, 5, 6, 7, 8]);
		expect(b.lastId).toBe(8);
	});
	it('defaults to a 5000-line cap', () => {
		const b = new LineBuffer();
		for (let i = 0; i < 5100; i++) b.push(String(i));
		expect(b.length).toBe(5000);
		expect(b.toArray()[0].text).toBe('100');
	});
	it('clear empties but ids keep increasing', () => {
		const b = new LineBuffer(3);
		b.push('a');
		b.clear();
		b.push('b');
		expect(b.toArray()).toEqual([{ id: 2, text: 'b' }]);
	});
});

describe('filter', () => {
	const lines = ['Server READY', 'player joined', 'ready again'].map((text, i) => ({ id: i + 1, text }));
	it('is a case-insensitive substring match', () => {
		expect(matchesFilter('Server READY', 'ready')).toBe(true);
		expect(matchesFilter('abc', '')).toBe(true);
		expect(matchesFilter('abc', 'x')).toBe(false);
		expect(visibleLines(lines, { query: 'READY' }).map((l) => l.id)).toEqual([1, 3]);
	});
	it('limits to uptoId and max', () => {
		expect(visibleLines(lines, { uptoId: 2 }).map((l) => l.id)).toEqual([1, 2]);
		expect(visibleLines(lines, { max: 2 }).map((l) => l.id)).toEqual([2, 3]);
	});
	it('highlights every match, preserving original case', () => {
		expect(highlightSegments('Foo foo', 'FOO')).toEqual([
			{ text: 'Foo', match: true },
			{ text: ' ', match: false },
			{ text: 'foo', match: true }
		]);
		expect(highlightSegments('abc', '')).toEqual([{ text: 'abc', match: false }]);
		expect(highlightSegments('abc', 'z')).toEqual([{ text: 'abc', match: false }]);
	});
});

describe('scroll + url helpers', () => {
	it('detects bottom', () => {
		expect(isAtBottom({ scrollHeight: 1000, scrollTop: 596, clientHeight: 400 })).toBe(true);
		expect(isAtBottom({ scrollHeight: 1000, scrollTop: 100, clientHeight: 400 })).toBe(false);
	});
	it('builds urls', () => {
		expect(logsUrl('/api/v1', 'a b', 0)).toBe('/api/v1/instances/a%20b/logs?tail=0');
	});
});

class FakeES implements LogEventSource {
	onopen: ((e: Event) => unknown) | null = null;
	onerror: ((e: Event) => unknown) | null = null;
	closed = false;
	listeners = new Map<string, (e: Event) => void>();
	constructor(public url: string) {}
	addEventListener(t: string, l: (e: Event) => void) {
		this.listeners.set(t, l);
	}
	close() {
		this.closed = true;
	}
	emit(type: string, data: string) {
		this.listeners.get(type)?.({ data } as unknown as Event);
	}
}

describe('createLogStream reconnect', () => {
	beforeEach(() => vi.useFakeTimers());
	afterEach(() => vi.useRealTimers());

	function setup() {
		const sources: FakeES[] = [];
		const lines: string[] = [];
		const states: string[] = [];
		const statuses: string[] = [];
		const s = createLogStream({
			url: (tail) => logsUrl('/api/v1', 'i1', tail),
			make: (u) => {
				const e = new FakeES(u);
				sources.push(e);
				return e;
			},
			onLine: (l) => lines.push(l),
			onState: (st) => states.push(st),
			onStatus: (st) => statuses.push(st)
		});
		return { s, sources, lines, states, statuses };
	}

	it('uses tail=500 first, then tail=0 after the first successful connection', () => {
		expect(logsTail(false)).toBe(500);
		expect(logsTail(true)).toBe(0);
		const { sources, lines, states } = setup();
		expect(sources[0].url).toMatch(/tail=500$/);
		sources[0].onopen?.(new Event('open'));
		sources[0].emit('log', 'hello');
		sources[0].emit('state', 'running');
		sources[0].emit('state', 'bogus');
		expect(lines).toEqual(['hello']);
		expect(states).toEqual(['running']);

		sources[0].onerror?.(new Event('error'));
		expect(sources[0].closed).toBe(true);
		vi.advanceTimersByTime(1000);
		expect(sources).toHaveLength(2);
		expect(sources[1].url).toMatch(/tail=0$/);

		// a second drop keeps using tail=0
		sources[1].onopen?.(new Event('open'));
		sources[1].onerror?.(new Event('error'));
		vi.advanceTimersByTime(1000);
		expect(sources[2].url).toMatch(/tail=0$/);
	});

	it('keeps asking for the tail if it never connected, and backs off', () => {
		const { sources } = setup();
		sources[0].onerror?.(new Event('error'));
		vi.advanceTimersByTime(1000);
		expect(sources[1].url).toMatch(/tail=500$/);
		sources[1].onerror?.(new Event('error'));
		vi.advanceTimersByTime(1999);
		expect(sources).toHaveLength(2);
		vi.advanceTimersByTime(1);
		expect(sources).toHaveLength(3);
	});

	it('close() stops reconnecting and ignores late events', () => {
		const { s, sources, lines, statuses } = setup();
		sources[0].onopen?.(new Event('open'));
		s.close();
		expect(sources[0].closed).toBe(true);
		sources[0].emit('log', 'late');
		expect(lines).toEqual([]);
		vi.advanceTimersByTime(60000);
		expect(sources).toHaveLength(1);
		expect(statuses.at(-1)).toBe('closed');
	});
});
