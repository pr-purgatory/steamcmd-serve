import type { Middleware } from 'openapi-fetch';

const SAFE_METHODS = new Set(['GET', 'HEAD', 'OPTIONS']);

/**
 * Adds `X-CSRF-Token` to every non-safe request. The token is loaded lazily and
 * cached; call `reset()` after login/logout/401 so the next mutation refetches.
 * If no token can be loaded (e.g. login/setup before a session exists) the
 * request is sent without the header.
 */
export function createCsrfMiddleware(loadToken: () => Promise<string | null>) {
	let cached: string | null = null;

	const middleware: Middleware = {
		async onRequest({ request }) {
			if (SAFE_METHODS.has(request.method.toUpperCase())) return undefined;
			if (cached === null) {
				try {
					cached = await loadToken();
				} catch {
					cached = null;
				}
			}
			if (cached) request.headers.set('X-CSRF-Token', cached);
			return request;
		}
	};

	return {
		middleware,
		reset() {
			cached = null;
		}
	};
}
