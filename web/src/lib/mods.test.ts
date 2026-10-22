import createClient from 'openapi-fetch';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { InstalledMod } from './api/types';
import type { paths } from './api/schema';
import {
	buildUploadForm,
	canChangeMods,
	debounce,
	findInstalled,
	isManualDownloadFailure,
	nexusStatus,
	normalizeQuery,
	putNexusKey,
	requiredByLabel,
	uploadProblem
} from './mods.ts';

const FAKE_KEY = 'test-key-not-real';

const mod = (over: Partial<InstalledMod>): InstalledMod => ({
	id: 'x',
	provider: 'thunderstore',
	ref: { id: 'x', version: '1.0.0' },
	sha256: 'ab',
	explicit: true,
	requiredBy: [],
	installedAt: '2026-10-01T00:00:00Z',
	...over
});

describe('findInstalled', () => {
	const list = [
		mod({ id: 'A-B', ref: { id: 'A-B', version: '1' } }),
		mod({ id: '42', provider: 'nexus', ref: { id: '42', version: '2' } })
	];
	it('matches provider and id together', () => {
		expect(findInstalled(list, 'thunderstore', 'A-B')?.id).toBe('A-B');
		expect(findInstalled(list, 'nexus', '42')?.id).toBe('42');
		expect(findInstalled(list, 'nexus', 'A-B')).toBeUndefined();
		expect(findInstalled(list, 'thunderstore', '42')).toBeUndefined();
		// Nexus: installed as "<modId>:<fileId>", searched as "<modId>".
		const nexus = [mod({ id: 'x1', provider: 'nexus', ref: { id: '42:777', version: '2' } })];
		expect(findInstalled(nexus, 'nexus', '42')?.id).toBe('x1');
		expect(findInstalled(nexus, 'nexus', '421')).toBeUndefined();
	});
	it('labels dependencies', () => {
		const dep = mod({ id: 'D', explicit: false, requiredBy: ['A-B', 'gone'] });
		expect(requiredByLabel(dep, [...list, dep])).toBe('dependency of A-B, gone');
	});
});

describe('manual download detection', () => {
	it('detects the server message', () => {
		expect(isManualDownloadFailure('NexusMods requires a manual download for free accounts')).toBe(true);
		expect(isManualDownloadFailure('Manual Download required')).toBe(true);
	});
	it('ignores other failures', () => {
		expect(isManualDownloadFailure('network error')).toBe(false);
		expect(isManualDownloadFailure(undefined)).toBe(false);
		expect(isManualDownloadFailure('')).toBe(false);
	});
});

describe('upload', () => {
	it('puts fields in order provider, ref, file last', () => {
		const f = buildUploadForm('nexus', { id: '42', version: '1.2' }, new Blob(['zip']), 'mod.zip');
		expect([...f.keys()]).toEqual(['provider', 'ref', 'file']);
		expect(f.get('provider')).toBe('nexus');
		expect(JSON.parse(f.get('ref') as string)).toEqual({ id: '42', version: '1.2' });
		expect((f.get('file') as File).name).toBe('mod.zip');
	});
	it('validates the file', () => {
		expect(uploadProblem(null)).toMatch(/Choose/);
		expect(uploadProblem({ name: 'a.rar', size: 5 })).toMatch(/zip/);
		expect(uploadProblem({ name: 'a.ZIP', size: 5 })).toBeNull();
		expect(uploadProblem({ name: 'a.zip', size: 1024 ** 3 + 1 })).toMatch(/1 GiB/);
	});
});

describe('state gate', () => {
	it('allows only stopped and error', () => {
		expect(canChangeMods('stopped')).toBe(true);
		expect(canChangeMods('error')).toBe(true);
		expect(canChangeMods('running')).toBe(false);
		expect(canChangeMods('new')).toBe(false);
	});
});

describe('normalizeQuery', () => {
	it('empty, too short, ok', () => {
		expect(normalizeQuery('  ')).toBe('');
		expect(normalizeQuery('a')).toBeNull();
		expect(normalizeQuery(' ab ')).toBe('ab');
	});
});

describe('debounce', () => {
	beforeEach(() => vi.useFakeTimers());
	afterEach(() => vi.useRealTimers());
	it('fires once with the last args after the delay', () => {
		const fn = vi.fn();
		const d = debounce(fn, 400);
		d('a');
		vi.advanceTimersByTime(300);
		d('ab');
		vi.advanceTimersByTime(399);
		expect(fn).not.toHaveBeenCalled();
		vi.advanceTimersByTime(1);
		expect(fn).toHaveBeenCalledOnce();
		expect(fn).toHaveBeenCalledWith('ab');
	});
	it('cancel drops the pending call', () => {
		const fn = vi.fn();
		const d = debounce(fn, 400);
		d();
		d.cancel();
		vi.advanceTimersByTime(1000);
		expect(fn).not.toHaveBeenCalled();
	});
});

describe('nexus key', () => {
	it('status text', () => {
		expect(nexusStatus(null)).toBe('Not set');
		expect(nexusStatus({ hasValue: false })).toBe('Not set');
		expect(nexusStatus({ hasValue: true, username: 'bob', premium: true })).toBe('Connected as bob (Premium)');
		expect(nexusStatus({ hasValue: true, username: 'bob', premium: false })).toBe('Connected as bob (Free)');
	});
	it('PUT sends the key in the body only, never in the URL', async () => {
		let seen: Request | undefined;
		let bodyText = '';
		const client = createClient<paths>({
			baseUrl: 'http://x/api/v1',
			fetch: async (req) => {
				seen = req;
				bodyText = await req.clone().text();
				return new Response(JSON.stringify({ hasValue: true, username: 'u', premium: false }), {
					status: 200,
					headers: { 'Content-Type': 'application/json' }
				});
			}
		});
		const { data } = await putNexusKey(client, FAKE_KEY);
		expect(data?.hasValue).toBe(true);
		expect(seen?.method).toBe('PUT');
		expect(seen?.url).toBe('http://x/api/v1/settings/nexus-api-key');
		expect(seen?.url).not.toContain(FAKE_KEY);
		expect(JSON.parse(bodyText)).toEqual({ apiKey: FAKE_KEY });
		expect(JSON.stringify(data)).not.toContain(FAKE_KEY);
	});
});
