import createClient from 'openapi-fetch';
import { describe, expect, it } from 'vitest';
import { createCsrfMiddleware } from './csrf.ts';
import { mockFetch } from './mock.ts';
import type { paths } from './schema';

function mockClient() {
	const csrf = createCsrfMiddleware(async () => {
		const r = await mockFetch(new Request('http://x/api/v1/auth/session'));
		return r.ok ? ((await r.json()) as { csrfToken: string }).csrfToken : null;
	});
	const client = createClient<paths>({
		baseUrl: 'http://x/api/v1',
		fetch: (req) => mockFetch(req)
	});
	client.use(csrf.middleware);
	return client;
}

describe('mock API', () => {
	it('requires login, then lists seeded games and instances', async () => {
		const c = mockClient();
		expect((await c.GET('/games')).response.status).toBe(401);
		expect((await c.POST('/auth/login', { body: { username: 'a', password: 'b' } })).response.status).toBe(204);
		const games = (await c.GET('/games')).data ?? [];
		expect(games.map((g) => g.id)).toEqual(['enshrouded', 'valheim']);
		expect(((await c.GET('/instances')).data ?? []).length).toBe(2);
	});

	it('returns 409 for a port already in use', async () => {
		const c = mockClient();
		await c.POST('/auth/login', { body: { username: 'a', password: 'b' } });
		const { response, error } = await c.POST('/instances', {
			body: { name: 'Dup', gameId: 'enshrouded', ports: { query: 15637 } }
		});
		expect(response.status).toBe(409);
		expect(error?.code).toBe('port_in_use');
	});
});
