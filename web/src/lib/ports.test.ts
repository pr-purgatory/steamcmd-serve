import { describe, expect, it } from 'vitest';
import type { PortSpec } from './api/types.ts';
import {
	createPortsPayload,
	defaultInputs,
	resolvePorts,
	validateMemory,
	validateName,
	validatePorts
} from './ports.ts';

const valheim: PortSpec[] = [
	{ name: 'game', default: 2456, protocol: 'udp' },
	{ name: 'query', default: 2457, protocol: 'udp', derivedFrom: 'game', offset: 1 }
];
const enshrouded: PortSpec[] = [{ name: 'query', default: 15637, protocol: 'udp' }];
const twoUdp: PortSpec[] = [
	{ name: 'a', default: 3000, protocol: 'udp' },
	{ name: 'b', default: 3001, protocol: 'udp' },
	{ name: 'c', default: 3002, protocol: 'udp', derivedFrom: 'a', offset: 2 }
];

describe('defaults and derivation', () => {
	it('prefills only non-derived ports', () => {
		expect(defaultInputs(valheim)).toEqual({ game: '2456' });
		expect(defaultInputs(enshrouded)).toEqual({ query: '15637' });
	});

	it('shows derived port as base + offset (game 2456 -> query 2457)', () => {
		const rows = resolvePorts(valheim, defaultInputs(valheim));
		expect(rows.find((r) => r.name === 'query')).toMatchObject({ derived: true, port: 2457 });
	});

	it('updates derived port live', () => {
		const rows = resolvePorts(valheim, { game: '3000' });
		expect(rows.find((r) => r.name === 'query')?.port).toBe(3001);
	});

	it('derived port is null while base is invalid', () => {
		expect(resolvePorts(valheim, { game: 'abc' })[1].port).toBeNull();
	});
});

describe('validatePorts', () => {
	it('accepts defaults', () => {
		expect(validatePorts(valheim, defaultInputs(valheim))).toEqual({});
		expect(validatePorts(enshrouded, defaultInputs(enshrouded))).toEqual({});
	});

	it('rejects empty', () => {
		expect(validatePorts(enshrouded, { query: '' }).query).toBe('Required');
		expect(validatePorts(enshrouded, { query: '  ' }).query).toBe('Required');
	});

	it('rejects non-integers', () => {
		for (const v of ['abc', '1.5', '-3000', '3e3', '0x400', '+3000']) {
			expect(validatePorts(enshrouded, { query: v }).query, v).toBeTruthy();
		}
	});

	it('rejects out of range', () => {
		for (const v of ['0', '80', '1023', '65536', '99999', '1000000']) {
			expect(validatePorts(enshrouded, { query: v }).query, v).toMatch(/1024 and 65535/);
		}
		expect(validatePorts(enshrouded, { query: '1024' })).toEqual({});
		expect(validatePorts(enshrouded, { query: '65535' })).toEqual({});
	});

	it('flags a derived port that overflows the range', () => {
		const errs = validatePorts(valheim, { game: '65535' });
		expect(errs.query).toMatch(/outside/);
	});

	it('flags duplicates of the same port and protocol', () => {
		const errs = validatePorts(twoUdp, { a: '4000', b: '4000' });
		expect(errs.b).toMatch(/already used by a/);
		expect(errs.a).toBeUndefined();
	});

	it('allows the same number on different protocols', () => {
		const specs: PortSpec[] = [
			{ name: 'a', default: 3000, protocol: 'udp' },
			{ name: 'b', default: 3000, protocol: 'tcp' }
		];
		expect(validatePorts(specs, { a: '3000', b: '3000' })).toEqual({});
	});

	it('flags a user port equal to a derived port', () => {
		// c is derived: a + 2 = 3002; b typed as 3002 collides with it.
		const errs = validatePorts(twoUdp, { a: '3000', b: '3002' });
		expect(errs.c).toMatch(/already used by b/);
	});

	it('flags valheim game port colliding with derived query when a user port equals it', () => {
		const specs: PortSpec[] = [
			...valheim,
			{ name: 'extra', default: 2457, protocol: 'udp' }
		];
		const errs = validatePorts(specs, { game: '2456', extra: '2457' });
		expect(errs.extra).toMatch(/2457\/udp is already used by query/);
	});

	it('reports a derived dependency on an invalid base', () => {
		expect(validatePorts(valheim, { game: '' }).query).toMatch(/Depends on game/);
	});
});

describe('payload and other fields', () => {
	it('omits derived ports from the create payload', () => {
		expect(createPortsPayload(valheim, { game: '2456' })).toEqual({ game: 2456 });
	});
	it('validates memory', () => {
		expect(validateMemory('8192')).toBeNull();
		expect(validateMemory('127')).toMatch(/at least 128/);
		expect(validateMemory('')).toBeTruthy();
		expect(validateMemory('8.5')).toBeTruthy();
	});
	it('validates names', () => {
		expect(validateName('Valheim 1')).toBeNull();
		expect(validateName('')).toBe('Required');
		expect(validateName(' lead')).toBeTruthy();
		expect(validateName('-x')).toBeTruthy();
		expect(validateName('a'.repeat(65))).toBeTruthy();
		expect(validateName('a'.repeat(64))).toBeNull();
	});
});
