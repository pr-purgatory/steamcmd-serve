import createClient from 'openapi-fetch';
import type { paths } from './schema';
import { createCsrfMiddleware } from './csrf';
import { mockFetch } from './mock';

export const BASE_URL = '/api/v1';

const useMock = import.meta.env.VITE_MOCK === '1';

/** Fetch used for every API call; the in-memory mock when VITE_MOCK=1. */
const apiFetch: typeof fetch = useMock
	? (input, init) => mockFetch(input instanceof Request ? input : new Request(input, init))
	: (input, init) => fetch(input, init);

async function loadCsrfToken(): Promise<string | null> {
	const res = await apiFetch(`${BASE_URL}/auth/session`, { credentials: 'same-origin' });
	if (!res.ok) return null;
	const body = (await res.json()) as { csrfToken?: string };
	return body.csrfToken ?? null;
}

const csrf = createCsrfMiddleware(loadCsrfToken);

let unauthorizedHandler: () => void = () => {};
/** Called on any 401 from the API (the layout routes to /login). */
export function onUnauthorized(handler: () => void) {
	unauthorizedHandler = handler;
}

export const api = createClient<paths>({
	baseUrl: BASE_URL,
	credentials: 'same-origin',
	fetch: (request) => apiFetch(request)
});

api.use(csrf.middleware, {
	async onResponse({ response, request }) {
		const path = new URL(request.url, 'http://x').pathname;
		if (response.status === 401 && !path.endsWith('/auth/login') && !path.endsWith('/setup')) {
			csrf.reset();
			unauthorizedHandler();
		}
		if (path.endsWith('/auth/login') || path.endsWith('/auth/logout') || path.endsWith('/setup')) {
			csrf.reset();
		}
		return undefined;
	}
});

/** Best-effort human message from an Error body. */
export function errorMessage(error: unknown, fallback = 'Request failed'): string {
	if (error && typeof error === 'object' && 'message' in error) {
		const e = error as { message?: unknown; details?: { field?: string; message?: string }[] };
		const base = typeof e.message === 'string' ? e.message : fallback;
		const details = e.details?.map((d) => (d.field ? `${d.field}: ${d.message}` : d.message));
		return details && details.length ? `${base} (${details.join('; ')})` : base;
	}
	return fallback;
}

/** True when first-run setup is still pending (no admin yet). Unknown/failed -> false. */
export async function setupRequired(): Promise<boolean> {
	try {
		const { data } = await api.GET('/setup');
		return data?.required === true;
	} catch {
		return false;
	}
}
