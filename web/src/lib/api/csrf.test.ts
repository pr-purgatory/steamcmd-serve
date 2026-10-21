import createClient from 'openapi-fetch';
import { describe, expect, it, vi } from 'vitest';
import { createCsrfMiddleware } from './csrf.ts';
import type { paths } from './schema';

function setup(loadToken: () => Promise<string | null> = async () => 'tok-123') {
	const seen: Request[] = [];
	const csrf = createCsrfMiddleware(loadToken);
	const client = createClient<paths>({
		baseUrl: 'http://localhost/api/v1',
		fetch: async (req) => {
			seen.push(req);
			return new Response(null, { status: 204 });
		}
	});
	client.use(csrf.middleware);
	return { client, seen, csrf };
}

describe('CSRF middleware', () => {
	it('does not add the header on GET and does not load a token', async () => {
		const loadToken = vi.fn(async () => 'tok-123');
		const { client, seen } = setup(loadToken);
		await client.GET('/games');
		expect(seen[0].headers.get('X-CSRF-Token')).toBeNull();
		expect(loadToken).not.toHaveBeenCalled();
	});

	it('adds the header on POST, PUT, PATCH and DELETE', async () => {
		const { client, seen } = setup();
		await client.POST('/instances/{instanceId}/actions/{action}', {
			params: { path: { instanceId: 'a', action: 'start' } }
		});
		await client.PUT('/settings/nexus-api-key', { body: { apiKey: 'x' } });
		await client.PATCH('/instances/{instanceId}', {
			params: { path: { instanceId: 'a' } },
			body: { name: 'n' }
		});
		await client.DELETE('/instances/{instanceId}', { params: { path: { instanceId: 'a' } } });
		expect(seen.map((r) => r.method)).toEqual(['POST', 'PUT', 'PATCH', 'DELETE']);
		for (const r of seen) expect(r.headers.get('X-CSRF-Token')).toBe('tok-123');
	});

	it('caches the token across requests and refetches after reset', async () => {
		const loadToken = vi.fn(async () => 'tok-123');
		const { client, csrf } = setup(loadToken);
		const call = () =>
			client.POST('/instances/{instanceId}/actions/{action}', {
				params: { path: { instanceId: 'a', action: 'stop' } }
			});
		await call();
		await call();
		expect(loadToken).toHaveBeenCalledTimes(1);
		csrf.reset();
		await call();
		expect(loadToken).toHaveBeenCalledTimes(2);
	});

	it('sends without the header when no token is available', async () => {
		const { client, seen } = setup(vi.fn(async () => null));
		await client.POST('/auth/logout');
		expect(seen[0].headers.get('X-CSRF-Token')).toBeNull();
	});

	it('survives a failing token loader', async () => {
		const { client, seen } = setup(
			vi.fn(async () => {
				throw new Error('boom');
			})
		);
		await client.POST('/auth/logout');
		expect(seen).toHaveLength(1);
		expect(seen[0].headers.get('X-CSRF-Token')).toBeNull();
	});
});
