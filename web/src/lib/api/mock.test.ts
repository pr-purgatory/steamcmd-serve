import createClient from 'openapi-fetch';
import { describe, expect, it } from 'vitest';
import { createCsrfMiddleware } from './csrf.ts';
import { mockFetch } from './mock.ts';
import { buildUploadForm } from '../mods.ts';
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

	it('serves real settings schemas and masks secrets', async () => {
		const c = mockClient();
		await c.POST('/auth/login', { body: { username: 'a', password: 'b' } });
		const g = await c.GET('/games/{gameId}', { params: { path: { gameId: 'enshrouded' } } });
		expect((g.data?.settingsSchema as { title?: string }).title).toBe('Enshrouded server settings');
		const s = await c.GET('/instances/{instanceId}/settings', { params: { path: { instanceId: 'inst-1' } } });
		const groups = (s.data as { userGroups: { password: unknown }[] }).userGroups;
		expect(groups[0].password).toEqual({ hasValue: true });
		const bad = await c.PUT('/instances/{instanceId}/settings', {
			params: { path: { instanceId: 'inst-1' } },
			body: { nope: 1 }
		});
		expect(bad.response.status).toBe(400);
	});
});

describe('mock mods API', () => {
	async function login() {
		const c = mockClient();
		await c.POST('/auth/login', { body: { username: 'a', password: 'b' } });
		return c;
	}
	const FAKE_KEY = 'test-key-not-real';
	const p = { params: { path: { instanceId: 'inst-2' } } };

	it('nexus search needs a key; thunderstore lists deps', async () => {
		const c = await login();
		const ts = await c.GET('/games/{gameId}/mods', {
			params: { path: { gameId: 'valheim' }, query: { provider: 'thunderstore', q: 'epic' } }
		});
		expect(ts.data?.[0].dependencies?.length).toBeGreaterThan(0);
		const nx = await c.GET('/games/{gameId}/mods', {
			params: { path: { gameId: 'valheim' }, query: { provider: 'nexus' } }
		});
		expect(nx.response.status).toBe(400);
		expect(nx.error?.message).toMatch(/Settings/);
		expect((await c.PUT('/settings/nexus-api-key', { body: { apiKey: FAKE_KEY } })).data?.premium).toBe(false);
		const nx2 = await c.GET('/games/{gameId}/mods', {
			params: { path: { gameId: 'valheim' }, query: { provider: 'nexus' } }
		});
		expect(nx2.response.status).toBe(200);
		expect((await c.DELETE('/settings/nexus-api-key')).response.status).toBe(204);
		expect((await c.GET('/settings/nexus-api-key')).data?.hasValue).toBe(false);
	});

	it('installs with deps, refuses uninstalling a dependency, fails nexus without premium', async () => {
		const c = await login();
		const job = await c.POST('/instances/{instanceId}/mods', {
			...p,
			body: { provider: 'thunderstore', ref: { id: 'RandyKnapp-EpicLoot', version: '0.10.4' } }
		});
		expect(job.response.status).toBe(202);
		await new Promise((r) => setTimeout(r, 1700));
		const list = (await c.GET('/instances/{instanceId}/mods', p)).data ?? [];
		expect(list.find((m) => m.id === 'RandyKnapp-EpicLoot')?.explicit).toBe(true);
		expect(list.find((m) => m.id === 'ValheimModding-Jotunn')?.explicit).toBe(false);
		const del = await c.DELETE('/instances/{instanceId}/mods/{modId}', {
			params: { path: { instanceId: 'inst-2', modId: 'ValheimModding-Jotunn' } }
		});
		expect(del.response.status).toBe(409);

		const nx = await c.POST('/instances/{instanceId}/mods', {
			...p,
			body: { provider: 'nexus', ref: { id: '1000', version: '3.1.0' } }
		});
		await new Promise((r) => setTimeout(r, 700));
		const j = await c.GET('/jobs/{jobId}', { params: { path: { jobId: nx.data!.id } } });
		expect(j.data?.status).toBe('failed');
		expect(j.data?.message).toMatch(/manual download/);
	});

	it('accepts a multipart upload built by buildUploadForm', async () => {
		const c = await login();
		const form = buildUploadForm('nexus', { id: '1001', version: '0.9.2' }, new Blob(['zip']), 'qs.zip');
		const r = await c.POST('/instances/{instanceId}/mods', {
			...p,
			body: {} as never,
			bodySerializer: () => form
		});
		expect(r.response.status).toBe(202);
		const bad = await c.POST('/instances/{instanceId}/mods', {
			...p,
			body: {} as never,
			bodySerializer: () => buildUploadForm('nexus', { id: '1', version: '1' }, new Blob(['x']), 'x.rar')
		});
		expect(bad.response.status).toBe(400);
	});
});
