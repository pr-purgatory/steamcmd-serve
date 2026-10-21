/**
 * Dev-only fake EventSource for VITE_MOCK=1 (openapi-fetch cannot mock SSE).
 * Imported only behind the VITE_MOCK constant so it is dropped from production builds.
 */
import type { LogEventSource } from '../logs.ts';

const SAMPLES = [
	'[MOCKLOG] Server tick ok',
	'[MOCKLOG] Player joined the game',
	'[MOCKLOG] Autosave complete',
	'[MOCKLOG] WARN latency spike detected',
	'[MOCKLOG] Player left the game',
	'[MOCKLOG] World saved'
];

export function createMockEventSource(url: string): LogEventSource {
	const listeners = new Map<string, ((e: Event) => void)[]>();
	let closed = false;
	let n = 0;
	const tail = Number(new URL(url, 'http://mock').searchParams.get('tail') ?? '0');

	const es: LogEventSource = {
		onopen: null,
		onerror: null,
		addEventListener(type, l) {
			listeners.set(type, [...(listeners.get(type) ?? []), l]);
		},
		close() {
			closed = true;
			clearInterval(timer);
		}
	};
	const emit = (type: string, data: string) => {
		for (const l of listeners.get(type) ?? []) l({ data } as unknown as Event);
	};
	const line = () => emit('log', `${new Date().toISOString()} ${SAMPLES[n++ % SAMPLES.length]} #${n}`);

	const timer = setInterval(() => {
		if (closed) return;
		for (let i = 0; i < 3; i++) line();
	}, 1000);

	setTimeout(() => {
		if (closed) return;
		es.onopen?.(new Event('open'));
		emit('state', 'running');
		for (let i = 0; i < Math.min(tail, 20); i++) line();
	}, 100);

	return es;
}
