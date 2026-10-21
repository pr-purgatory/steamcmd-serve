/**
 * In-memory API used when VITE_MOCK=1 so the UI works without the Go backend.
 * It is a fetch-level fake: the real openapi-fetch client and middleware run on top.
 * All payloads are typed from the generated schema.
 */
import type { components } from './schema';
import enshroudedSchema from '../../../../games/enshrouded/settings.schema.json';
import valheimSchema from '../../../../games/valheim/settings.schema.json';
import { defaultsFor, isPlaceholder, type Json, type Schema } from '../config-form/logic.ts';

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

const settingsSchemas: Record<string, Schema> = {
	enshrouded: enshroudedSchema as unknown as Schema,
	valheim: valheimSchema as unknown as Schema
};

/** Mock-stored settings per instance id (real values; secrets are masked on read). */
const storedSettings = new Map<string, Record<string, Json>>();
const MOCK_SECRET = 'mock-secret-0123456789';

function maskSecrets(schema: Schema, v: Json): Json {
	if (schema['x-secret']) return { hasValue: true };
	if (Array.isArray(v) && schema.items) return v.map((x) => maskSecrets(schema.items as Schema, x));
	if (v && typeof v === 'object' && !Array.isArray(v) && schema.properties) {
		const out: Record<string, Json> = {};
		for (const [k, x] of Object.entries(v)) {
			const ps = schema.properties[k];
			out[k] = ps ? maskSecrets(ps, x) : x;
		}
		return out;
	}
	return v;
}

/** Replace {hasValue:true} by the stored value at the same path (arrays by index). */
function unmaskSecrets(schema: Schema, v: Json, old: Json | undefined): Json {
	if (schema['x-secret']) return isPlaceholder(v) ? (old ?? MOCK_SECRET) : v;
	if (Array.isArray(v) && schema.items) {
		const o = Array.isArray(old) ? old : [];
		return v.map((x, i) => unmaskSecrets(schema.items as Schema, x, o[i]));
	}
	if (v && typeof v === 'object' && !Array.isArray(v) && schema.properties) {
		const o = (old && typeof old === 'object' && !Array.isArray(old) ? old : {}) as Record<string, Json>;
		const out: Record<string, Json> = {};
		for (const [k, x] of Object.entries(v)) {
			const ps = schema.properties[k];
			out[k] = ps ? unmaskSecrets(ps, x, o[k]) : x;
		}
		return out;
	}
	return v;
}

function currentSettings(inst: S['Instance']): Record<string, Json> {
	let cur = storedSettings.get(inst.id);
	if (!cur) {
		// Defaults, with every secret filled so it reads back as {hasValue:true}.
		const schema = settingsSchemas[inst.gameId];
		cur = unmaskSecrets(schema, maskSecrets(schema, defaultsFor(schema)), undefined) as Record<string, Json>;
		storedSettings.set(inst.id, cur);
	}
	return cur;
}

/** Loose validation: object shape, known keys, basic types and enums at the top level. */
function looseValidate(schema: Schema, body: unknown): string | null {
	if (!body || typeof body !== 'object' || Array.isArray(body)) return 'Settings must be an object';
	for (const [k, v] of Object.entries(body as Record<string, unknown>)) {
		const ps = schema.properties?.[k];
		if (!ps) return `Unknown setting "${k}"`;
		if (ps.enum && !ps.enum.includes(v as Json)) return `${k}: must be one of ${ps.enum.join(', ')}`;
		if (ps.type === 'integer' && !Number.isInteger(v)) return `${k}: must be an integer`;
		if (ps.type === 'number' && typeof v !== 'number') return `${k}: must be a number`;
		if (ps.type === 'string' && typeof v !== 'string') return `${k}: must be a string`;
		if (ps.type === 'boolean' && typeof v !== 'boolean') return `${k}: must be a boolean`;
		if (ps.type === 'array' && !Array.isArray(v)) return `${k}: must be an array`;
	}
	return null;
}

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


// ---- backups + jobs (jobs finish ~2 s after creation) ----
const JOB_MS = 2000;
const backups = new Map<string, S['Backup'][]>();
const jobStarts = new Map<string, { job: S['Job']; startedAt: number }>();
let nextBackup = 1;
let nextJob = 1;

function backupsFor(instId: string): S['Backup'][] {
	let list = backups.get(instId);
	if (!list) {
		const now = Date.now();
		list = [
			{ id: `bk-${nextBackup++}`, createdAt: new Date(now - 3600e3).toISOString(), sizeBytes: 52_428_800, reason: 'scheduled' },
			{ id: `bk-${nextBackup++}`, createdAt: new Date(now - 86400e3).toISOString(), sizeBytes: 48_234_496, reason: 'pre-update' },
			{ id: `bk-${nextBackup++}`, createdAt: new Date(now - 3 * 86400e3).toISOString(), sizeBytes: 1_536, reason: 'manual' }
		];
		backups.set(instId, list);
	}
	return list;
}

function startBackupJob(inst: S['Instance'], kind: 'backup' | 'restore', reason: S['Backup']['reason']): S['Job'] {
	const job: S['Job'] = {
		id: `mockjob-${nextJob++}`,
		instanceId: inst.id,
		kind,
		status: 'running',
		progress: 0,
		createdAt: new Date().toISOString()
	};
	jobStarts.set(job.id, { job, startedAt: Date.now() });
	after(JOB_MS, () => {
		backupsFor(inst.id).unshift({
			id: `bk-${nextBackup++}`,
			createdAt: new Date().toISOString(),
			sizeBytes: 40_000_000 + Math.floor(Math.random() * 10_000_000),
			reason
		});
		job.finishedAt = new Date().toISOString();
	});
	return job;
}

function jobView(id: string): S['Job'] | null {
	const e = jobStarts.get(id);
	if (!e) return null;
	const done = Date.now() - e.startedAt >= JOB_MS;
	return {
		...e.job,
		status: done ? 'succeeded' : 'running',
		progress: done ? 1 : Math.min(0.95, (Date.now() - e.startedAt) / JOB_MS),
		message: done ? 'Done' : 'Working…'
	};
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
		const detail: S['GameDetail'] = { ...g, settingsSchema: settingsSchemas[g.id] ?? {} };
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

	m = path.match(/^\/jobs\/([^/]+)$/);
	if (m && method === 'GET') {
		const j = jobView(m[1]);
		return j ? json(200, j) : err(404, 'not_found', 'Job not found');
	}
	m = path.match(/^\/instances\/([^/]+)\/backups$/);
	if (m) {
		const inst = instances.find((i) => i.id === m![1]);
		if (!inst) return err(404, 'not_found', 'Instance not found');
		if (method === 'GET') return json(200, backupsFor(inst.id));
		if (method === 'POST') return json(202, startBackupJob(inst, 'backup', 'manual'));
	}
	m = path.match(/^\/instances\/([^/]+)\/backups\/([^/]+)\/restore$/);
	if (m && method === 'POST') {
		const inst = instances.find((i) => i.id === m![1]);
		if (!inst) return err(404, 'not_found', 'Instance not found');
		if (!backupsFor(inst.id).some((b) => b.id === m![2])) return err(404, 'not_found', 'Backup not found');
		if (inst.state !== 'stopped' && inst.state !== 'error')
			return err(409, 'bad_state', `Stop the server before restoring (currently ${inst.state})`);
		return json(202, startBackupJob(inst, 'restore', 'pre-restore'));
	}

	m = path.match(/^\/instances\/([^/]+)\/actions\/([^/]+)$/);
	if (m && method === 'POST') {
		const inst = instances.find((i) => i.id === m![1]);
		if (!inst) return err(404, 'not_found', 'Instance not found');
		return runAction(inst, m[2]);
	}
	m = path.match(/^\/instances\/([^/]+)\/settings$/);
	if (m) {
		const inst = instances.find((i) => i.id === m![1]);
		if (!inst) return err(404, 'not_found', 'Instance not found');
		const schema = settingsSchemas[inst.gameId];
		if (method === 'GET') return json(200, maskSecrets(schema, currentSettings(inst)));
		if (method === 'PUT') {
			const body = (await request.json()) as Json;
			const bad = looseValidate(schema, body);
			if (bad) return err(400, 'invalid', bad);
			const stored = unmaskSecrets(schema, body, currentSettings(inst)) as Record<string, Json>;
			storedSettings.set(inst.id, stored);
			return json(200, maskSecrets(schema, stored));
		}
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
