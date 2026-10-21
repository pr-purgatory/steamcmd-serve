/**
 * In-memory API used when VITE_MOCK=1 so the UI works without the Go backend.
 * It is a fetch-level fake: the real openapi-fetch client and middleware run on top.
 * All payloads are typed from the generated schema.
 */
import type { components } from './schema';

type S = components['schemas'];

export const mockGames: S['Game'][] = [
	{
		id: 'enshrouded',
		name: 'Enshrouded',
		platform: 'windows',
		serverAppId: 2278520,
		gameAppId: 1203620,
		ports: [{ name: 'query', default: 15637, protocol: 'udp', description: 'Game and query port' }],
		modLoader: 'none',
		resources: { memoryMB: 8192, diskGB: 30 }
	},
	{
		id: 'valheim',
		name: 'Valheim',
		platform: 'linux',
		serverAppId: 896660,
		gameAppId: 892970,
		ports: [
			{ name: 'game', default: 2456, protocol: 'udp', description: 'Game traffic' },
			{
				name: 'query',
				default: 2457,
				protocol: 'udp',
				derivedFrom: 'game',
				offset: 1,
				description: 'Steam server browser query'
			}
		],
		modLoader: 'bepinex',
		modProviders: ['thunderstore', 'nexus'],
		resources: { memoryMB: 4096, diskGB: 6 }
	}
];

const instances: S['Instance'][] = [
	{
		id: 'inst-1',
		name: 'Enshrouded Main',
		gameId: 'enshrouded',
		state: 'running',
		ready: true,
		ports: [{ name: 'query', port: 15637, protocol: 'udp' }],
		memoryMB: 8192,
		installedBuildId: '15012345',
		createdAt: '2026-10-01T12:00:00Z'
	},
	{
		id: 'inst-2',
		name: 'Valheim Friends',
		gameId: 'valheim',
		state: 'stopped',
		ready: false,
		ports: [
			{ name: 'game', port: 2456, protocol: 'udp' },
			{ name: 'query', port: 2457, protocol: 'udp' }
		],
		memoryMB: 4096,
		installedBuildId: '14998877',
		createdAt: '2026-10-05T18:30:00Z'
	}
];

const sessionKey = 'scs-mock-session';
const CSRF = 'mock-csrf-token';
let adminCreated = false;
let nextId = 3;
let memLoggedIn = false;

function loggedIn(): boolean {
	try {
		return memLoggedIn || sessionStorage.getItem(sessionKey) === '1';
	} catch {
		return memLoggedIn;
	}
}
function setLoggedIn(v: boolean) {
	memLoggedIn = v;
	try {
		if (v) sessionStorage.setItem(sessionKey, '1');
		else sessionStorage.removeItem(sessionKey);
	} catch {
		/* storage unavailable */
	}
}

function json(status: number, body: unknown): Response {
	return new Response(JSON.stringify(body), {
		status,
		headers: { 'Content-Type': 'application/json' }
	});
}
function empty(status: number): Response {
	return new Response(null, { status });
}
function err(status: number, code: string, message: string): Response {
	const body: S['Error'] = { code, message };
	return json(status, body);
}

function after(ms: number, fn: () => void) {
	setTimeout(fn, ms);
}

function runAction(inst: S['Instance'], action: string): Response {
	const job: S['Job'] = {
		id: `job-${Date.now()}`,
		instanceId: inst.id,
		kind: action as S['Job']['kind'],
		status: 'running',
		createdAt: new Date().toISOString()
	};
	switch (action) {
		case 'start':
			if (inst.state !== 'stopped') return err(409, 'bad_state', `Cannot start while ${inst.state}`);
			inst.state = 'starting';
			inst.ready = false;
			after(1500, () => {
				inst.state = 'running';
				inst.ready = true;
			});
			break;
		case 'stop':
			if (inst.state !== 'running' && inst.state !== 'starting')
				return err(409, 'bad_state', `Cannot stop while ${inst.state}`);
			inst.state = 'stopping';
			inst.ready = false;
			after(1500, () => (inst.state = 'stopped'));
			break;
		case 'restart':
			if (inst.state !== 'running') return err(409, 'bad_state', 'Instance is not running');
			inst.state = 'stopping';
			inst.ready = false;
			after(1000, () => {
				inst.state = 'starting';
				after(1500, () => {
					inst.state = 'running';
					inst.ready = true;
				});
			});
			break;
		case 'install':
		case 'update':
			if (inst.state !== 'new' && inst.state !== 'stopped' && inst.state !== 'error')
				return err(409, 'bad_state', `Cannot ${action} while ${inst.state}`);
			inst.state = action === 'install' ? 'installing' : 'updating';
			after(3000, () => (inst.state = 'stopped'));
			break;
		default:
			return err(404, 'not_found', 'Unknown action');
	}
	return json(202, job);
}

export async function mockFetch(request: Request): Promise<Response> {
	await new Promise((r) => setTimeout(r, 80));
	const url = new URL(request.url, 'http://mock');
	const path = url.pathname.replace(/^\/api\/v1/, '');
	const method = request.method.toUpperCase();

	if (path === '/setup' && method === 'POST') {
		if (adminCreated) return err(409, 'setup_done', 'Setup already completed');
		const body = (await request.json()) as S['Credentials'];
		if (body.password.length < 12) return err(400, 'invalid', 'Password too short');
		adminCreated = true;
		return empty(201);
	}
	if (path === '/auth/login' && method === 'POST') {
		const body = (await request.json()) as S['Credentials'];
		if (!body.username || !body.password) return err(401, 'unauthorized', 'Invalid credentials');
		setLoggedIn(true);
		return empty(204);
	}

	if (!loggedIn()) return err(401, 'unauthorized', 'Not logged in');

	if (path === '/auth/logout' && method === 'POST') {
		setLoggedIn(false);
		return empty(204);
	}
	if (path === '/auth/session' && method === 'GET') {
		const body: S['Session'] = { username: 'admin', csrfToken: CSRF };
		return json(200, body);
	}

	// Every remaining mutation needs the CSRF header, like the real server.
	if (method !== 'GET' && request.headers.get('X-CSRF-Token') !== CSRF)
		return err(403, 'csrf', 'Missing or invalid CSRF token');

	if (path === '/games' && method === 'GET') return json(200, mockGames);
	let m = path.match(/^\/games\/([^/]+)$/);
	if (m && method === 'GET') {
		const g = mockGames.find((x) => x.id === m![1]);
		if (!g) return err(404, 'not_found', 'Game not found');
		const detail: S['GameDetail'] = { ...g, settingsSchema: {} };
		return json(200, detail);
	}

	if (path === '/instances' && method === 'GET') return json(200, instances);
	if (path === '/instances' && method === 'POST') {
		const body = (await request.json()) as S['InstanceCreate'];
		const game = mockGames.find((g) => g.id === body.gameId);
		if (!game) return err(400, 'invalid', 'Unknown game');
		const ports: S['PortBinding'][] = [];
		for (const spec of game.ports) {
			if (spec.derivedFrom) continue;
			ports.push({
				name: spec.name,
				port: body.ports?.[spec.name] ?? spec.default,
				protocol: spec.protocol
			});
		}
		for (const spec of game.ports) {
			if (!spec.derivedFrom) continue;
			const base = ports.find((p) => p.name === spec.derivedFrom);
			if (base) ports.push({ name: spec.name, port: base.port + (spec.offset ?? 0), protocol: spec.protocol });
		}
		for (const p of ports) {
			const clash = instances.find((i) =>
				i.ports.some((q) => q.port === p.port && q.protocol === p.protocol)
			);
			if (clash)
				return err(409, 'port_in_use', `Port ${p.port}/${p.protocol} is already used by "${clash.name}"`);
		}
		const inst: S['Instance'] = {
			id: `inst-${nextId++}`,
			name: body.name,
			gameId: body.gameId,
			state: 'new',
			ready: false,
			ports,
			memoryMB: body.memoryMB ?? game.resources.memoryMB,
			createdAt: new Date().toISOString()
		};
		instances.push(inst);
		return json(201, inst);
	}

	m = path.match(/^\/instances\/([^/]+)\/actions\/([^/]+)$/);
	if (m && method === 'POST') {
		const inst = instances.find((i) => i.id === m![1]);
		if (!inst) return err(404, 'not_found', 'Instance not found');
		return runAction(inst, m[2]);
	}
	m = path.match(/^\/instances\/([^/]+)$/);
	if (m) {
		const idx = instances.findIndex((i) => i.id === m![1]);
		if (idx < 0) return err(404, 'not_found', 'Instance not found');
		if (method === 'GET') return json(200, instances[idx]);
		if (method === 'DELETE') {
			if (instances[idx].state !== 'stopped' && instances[idx].state !== 'new')
				return err(409, 'bad_state', 'Instance must be stopped');
			instances.splice(idx, 1);
			return empty(204);
		}
	}

	return err(404, 'not_found', `Mock has no handler for ${method} ${path}`);
}
