import enshroudedJson from '../../../../games/enshrouded/settings.schema.json';
import valheimJson from '../../../../games/valheim/settings.schema.json';
import { describe, expect, it } from 'vitest';
import {
	defaultsFor,
	groupProperties,
	isEnabled,
	minToNs,
	newItem,
	nsToMin,
	toFormValue,
	toPayload,
	toValidationValue,
	validateForm,
	validateSettings,
	type Json,
	type Schema
} from './logic.ts';

const enshrouded = enshroudedJson as unknown as Schema;
const valheim = valheimJson as unknown as Schema;

type Obj = Record<string, Json>;

describe('ns <-> minutes', () => {
	it('converts', () => {
		expect(nsToMin(600000000000)).toBe(10);
		expect(minToNs(10)).toBe(600000000000);
		expect(minToNs(12.5)).toBe(750000000000);
	});
	it('round-trips and always yields integers', () => {
		expect(minToNs(nsToMin(3600000000000))).toBe(3600000000000);
		for (const m of [0.1, 0.3, 2.0000001, 33.333333, 59.9999999]) {
			expect(Number.isInteger(minToNs(m))).toBe(true);
		}
		expect(minToNs(0.1 + 0.2)).toBe(18000000000);
	});
});

describe('secrets', () => {
	const server: Obj = {
		...(defaultsFor(enshrouded) as Obj),
		userGroups: [
			{ name: 'Admin', password: { hasValue: true }, canKickBan: true, canAccessInventories: true, canEditWorld: true, canEditBase: true, canExtendBase: true, reservedSlots: 0 },
			{ name: 'Guest', password: { hasValue: true }, canKickBan: false, canAccessInventories: false, canEditWorld: true, canEditBase: false, canExtendBase: false, reservedSlots: 0 }
		]
	};
	const groups = (v: Json) => (v as Obj).userGroups as Obj[];

	it('never prefills: placeholders become blank in the form', () => {
		const form = toFormValue(enshrouded, server);
		expect(groups(form).map((g) => g.password)).toEqual(['', '']);
	});
	it('blank -> {hasValue:true}, typed -> string, inside userGroups', () => {
		const form = toFormValue(enshrouded, server) as Obj;
		groups(form)[1].password = 'typed-secret';
		const out = toPayload(enshrouded, form, server);
		expect(groups(out)[0].password).toEqual({ hasValue: true });
		expect(groups(out)[1].password).toBe('typed-secret');
	});
	it('untouched form produces payload identical to the server value', () => {
		expect(toPayload(enshrouded, toFormValue(enshrouded, server), server)).toEqual(server);
	});
	it('top-level secret (valheim) and new item without stored value stays a string', () => {
		const vs = { name: 'V', world: 'W', password: { hasValue: true }, public: false };
		expect((toPayload(valheim, toFormValue(valheim, vs), vs) as Obj).password).toEqual({ hasValue: true });
		const form = toFormValue(enshrouded, server) as Obj;
		groups(form).push(newItem(enshrouded.properties!.userGroups) as Obj);
		expect(groups(toPayload(enshrouded, form, server))[2].password).toBe('');
	});
	it('validation substitutes a dummy of valid length for pending placeholders', () => {
		const form = toFormValue(valheim, { name: 'V', world: 'W', password: { hasValue: true } });
		const v = toValidationValue(valheim, form, { name: 'V', world: 'W', password: { hasValue: true } }) as Obj;
		expect(typeof v.password).toBe('string');
		expect((v.password as string).length).toBeGreaterThanOrEqual(5);
		expect(validateSettings(valheim, toValidationValue(valheim, { name: 'V', world: 'W', password: { hasValue: true } }, {}))).toEqual({});
	});
});

describe('x-enabled-when', () => {
	const gs = enshrouded.properties!.gameSettings;
	it('gameSettings disabled unless preset is Custom', () => {
		expect(isEnabled(gs, { gameSettingsPreset: 'Default' })).toBe(false);
		expect(isEnabled(gs, { gameSettingsPreset: 'Custom' })).toBe(true);
		expect(isEnabled(enshrouded.properties!.name, {})).toBe(true);
	});
});

describe('defaults and groups', () => {
	it('new userGroups item is built from the item schema', () => {
		expect(newItem(enshrouded.properties!.userGroups)).toEqual({
			name: '', password: '', canKickBan: false, canAccessInventories: false,
			canEditWorld: false, canEditBase: false, canExtendBase: false, reservedSlots: 0
		});
	});
	it('groups in first-seen order, ungrouped under General', () => {
		expect(groupProperties(enshrouded).map((g) => g.name)).toEqual(['server', 'chat', 'difficulty', 'access']);
		expect(groupProperties(valheim).map((g) => g.name)).toEqual(['General']);
	});
});

describe('schema validation', () => {
	const defaults = () => {
		const d = defaultsFor(enshrouded) as Obj;
		// default groups have blank passwords: generated at creation, so fill them
		(d.userGroups as Obj[]).forEach((g, i) => (g.password = `pw-${i}`));
		return d;
	};
	it('full Enshrouded defaults pass', () => {
		expect(validateSettings(enshrouded, defaults())).toEqual({});
		expect(validateForm(enshrouded, defaults(), defaults())).toEqual({});
	});
	it('slotCount 17 fails at /slotCount', () => {
		const e = validateSettings(enshrouded, { ...defaults(), slotCount: 17 });
		expect(Object.keys(e)).toEqual(['/slotCount']);
	});
	it('tombstoneMode "x" fails at /gameSettings/tombstoneMode', () => {
		const d = defaults();
		(d.gameSettings as Obj).tombstoneMode = 'x';
		expect(Object.keys(validateSettings(enshrouded, d))).toEqual(['/gameSettings/tombstoneMode']);
	});
	it('flags unknown keys, not declared ones', () => {
		const e = validateSettings(enshrouded, { ...defaults(), bogus: 1 });
		expect(Object.keys(e)).toEqual(['/bogus']);
		expect(e['/bogus']).toBe('Unknown setting');
	});
	it('reports minute limits in minutes, and missing item fields by path', () => {
		const d = defaults();
		(d.gameSettings as Obj).dayTimeDuration = 1;
		expect(validateSettings(enshrouded, d)['/gameSettings/dayTimeDuration']).toBe('Must be at least 2 min');
		const d2 = defaults();
		delete (d2.userGroups as Obj[])[0].name;
		expect(validateSettings(enshrouded, d2)['/userGroups/0/name']).toBe('Required');
	});
});

describe('validation without code generation', () => {
	it('never touches Function or eval (CSP has no unsafe-eval)', () => {
		const realFunction = globalThis.Function;
		const realEval = globalThis.eval;
		const boom = () => {
			throw new Error('dynamic code evaluation attempted');
		};
		globalThis.Function = boom as unknown as FunctionConstructor;
		globalThis.eval = boom;
		try {
			const d = defaultsFor(enshrouded) as Obj;
			(d.userGroups as Obj[]).forEach((g, i) => (g.password = `pw-${i}`));
			const fresh = JSON.parse(JSON.stringify(enshrouded)) as typeof enshrouded; // uncached validator
			expect(validateSettings(fresh, d)).toEqual({});
			expect(Object.keys(validateSettings(fresh, { ...d, slotCount: 17 }))).toEqual(['/slotCount']);
		} finally {
			globalThis.Function = realFunction;
			globalThis.eval = realEval;
		}
	});
});

describe('validation with an immutable schema', () => {
	it('does not modify the schema it is given (Svelte $state proxies forbid it)', () => {
		const deepFreeze = <T>(o: T): T => {
			if (o && typeof o === 'object') {
				Object.values(o).forEach(deepFreeze);
				Object.freeze(o);
			}
			return o;
		};
		const frozen = deepFreeze(JSON.parse(JSON.stringify(enshroudedJson)));
		expect(() => validateSettings(frozen, defaultsFor(frozen))).not.toThrow();
		expect(validateSettings(frozen, { ...(defaultsFor(frozen) as Obj), slotCount: 17 })['/slotCount']).toBeTruthy();
	});
});
