/**
 * Pure helpers for the schema-driven settings editor. Fully generic: nothing
 * here knows about a particular game. Custom schema keywords honoured:
 * x-ui-group, x-unit / x-display-unit, x-secret, x-generate, x-enabled-when.
 */
import Ajv2020 from 'ajv/dist/2020.js';

export type Json = null | boolean | number | string | Json[] | { [k: string]: Json };

export interface Schema {
	type?: string;
	title?: string;
	description?: string;
	default?: Json;
	enum?: Json[];
	minimum?: number;
	maximum?: number;
	minLength?: number;
	maxLength?: number;
	minItems?: number;
	pattern?: string;
	properties?: Record<string, Schema>;
	items?: Schema;
	required?: string[];
	'x-ui-group'?: string;
	'x-unit'?: string;
	'x-display-unit'?: string;
	'x-secret'?: boolean;
	'x-generate'?: string;
	'x-enabled-when'?: Record<string, Json>;
	[k: string]: unknown;
}

export const NS_PER_MIN = 60e9;
export const GENERAL_GROUP = 'General';

// ---------- units ----------

export function isMinuteUnit(s: Schema): boolean {
	return s['x-unit'] === 'ns' && s['x-display-unit'] === 'min';
}
export function nsToMin(ns: number): number {
	return ns / NS_PER_MIN;
}
/** Always an integer number of nanoseconds. */
export function minToNs(min: number): number {
	return Math.round(min * NS_PER_MIN);
}

// ---------- secrets ----------

export function isPlaceholder(v: unknown): boolean {
	return (
		typeof v === 'object' &&
		v !== null &&
		!Array.isArray(v) &&
		(v as Record<string, unknown>).hasValue === true &&
		Object.keys(v).length === 1
	);
}

const clone = <T>(v: T): T => (v === undefined ? v : (JSON.parse(JSON.stringify(v)) as T));

function dummySecret(s: Schema): string {
	const n = Math.max(s.minLength ?? 1, 1);
	return 'x'.repeat(typeof s.maxLength === 'number' ? Math.min(n, s.maxLength) : n);
}

type Mapper = (schema: Schema, value: Json, original: Json | undefined) => Json | undefined;

/** Walk value alongside schema and original; the mapper may replace secret leaves. */
function mapSecrets(schema: Schema, value: Json, original: Json | undefined, f: Mapper): Json {
	if (schema['x-secret']) return f(schema, value, original) ?? value;
	if (Array.isArray(value) && schema.items) {
		const orig = Array.isArray(original) ? original : [];
		return value.map((v, i) => mapSecrets(schema.items as Schema, v, orig[i], f));
	}
	if (value && typeof value === 'object' && !Array.isArray(value) && schema.properties) {
		const orig =
			original && typeof original === 'object' && !Array.isArray(original)
				? (original as Record<string, Json>)
				: {};
		const out: Record<string, Json> = {};
		for (const [k, v] of Object.entries(value)) {
			const ps = schema.properties[k];
			out[k] = ps ? mapSecrets(ps, v, orig[k], f) : v;
		}
		return out;
	}
	return value;
}

/** Server value -> editable form value: secret placeholders become '' (never pre-filled). */
export function toFormValue(schema: Schema, server: Json): Json {
	return mapSecrets(schema, clone(server), undefined, (_s, v) => (isPlaceholder(v) ? '' : undefined));
}

/**
 * Form value -> PUT body. A blank secret whose original (same path, arrays matched by
 * index) held a value is sent as {"hasValue": true}; anything typed is sent as a string.
 */
export function toPayload(schema: Schema, form: Json, original: Json): Json {
	return mapSecrets(schema, clone(form), original, (_s, v, orig) =>
		v === '' && isPlaceholder(orig) ? { hasValue: true } : undefined
	);
}

/** Value used for client-side validation: pending placeholders become a dummy valid string. */
export function toValidationValue(schema: Schema, form: Json, original: Json): Json {
	return mapSecrets(schema, clone(form), original, (s, v, orig) =>
		v === '' && isPlaceholder(orig) ? dummySecret(s) : isPlaceholder(v) ? dummySecret(s) : undefined
	);
}

/** Random value from the CSPRNG (unbiased via rejection sampling). */
export function generatePassword(length = 20): string {
	const alphabet = 'ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789';
	const limit = 256 - (256 % alphabet.length);
	let out = '';
	while (out.length < length) {
		const buf = new Uint8Array(length * 2);
		crypto.getRandomValues(buf);
		for (const b of buf) {
			if (b < limit && out.length < length) out += alphabet[b % alphabet.length];
		}
	}
	return out;
}

// ---------- defaults, groups, enabling ----------

/** Default value for a (sub)schema: its `default`, else a sensible empty value. */
export function defaultFor(s: Schema): Json {
	if (s.default !== undefined) return clone(s.default);
	if (s.enum && s.enum.length) return s.enum[0];
	switch (s.type) {
		case 'object': {
			const o: Record<string, Json> = {};
			for (const [k, ps] of Object.entries(s.properties ?? {})) o[k] = defaultFor(ps);
			return o;
		}
		case 'array':
			return [];
		case 'string':
			return '';
		case 'integer':
		case 'number':
			return s.minimum ?? 0;
		case 'boolean':
			return false;
		default:
			return null;
	}
}

/** A fresh array item, built from the item schema. */
export function newItem(arraySchema: Schema): Json {
	return defaultFor(arraySchema.items ?? {});
}

export function defaultsFor(schema: Schema): Json {
	return defaultFor({ ...schema, default: undefined });
}

export interface Group {
	name: string;
	keys: string[];
}

/** Top-level properties grouped by x-ui-group in first-seen order; ungrouped -> "General". */
export function groupProperties(schema: Schema): Group[] {
	const groups: Group[] = [];
	for (const [key, ps] of Object.entries(schema.properties ?? {})) {
		const name = ps['x-ui-group'] || GENERAL_GROUP;
		let g = groups.find((x) => x.name === name);
		if (!g) groups.push((g = { name, keys: [] }));
		g.keys.push(key);
	}
	return groups;
}

export function groupLabel(name: string): string {
	return name === GENERAL_GROUP ? name : name.charAt(0).toUpperCase() + name.slice(1);
}

/** x-enabled-when: every listed sibling must equal the given value. */
export function isEnabled(prop: Schema, root: Record<string, Json>): boolean {
	const cond = prop['x-enabled-when'];
	if (!cond) return true;
	return Object.entries(cond).every(([k, v]) => JSON.stringify(root[k]) === JSON.stringify(v));
}

export function sameJson(a: unknown, b: unknown): boolean {
	return JSON.stringify(a) === JSON.stringify(b);
}

/** Slider step: a power of ten giving about 100-1000 positions. */
export function niceStep(s: Schema, integer: boolean): number {
	if (integer) return 1;
	const range = (s.maximum ?? 1) - (s.minimum ?? 0);
	if (!(range > 0)) return 0.01;
	return Math.pow(10, Math.floor(Math.log10(range / 100)));
}

// ---------- validation ----------

export type FieldErrors = Record<string, string>;

const ajv = new Ajv2020({ allErrors: true, strict: false });
const compiled = new WeakMap<object, ReturnType<typeof ajv.compile>>();

function schemaAt(root: Schema, path: string): Schema | undefined {
	let cur: Schema | undefined = root;
	for (const seg of path.split('/').filter(Boolean)) {
		if (!cur) return undefined;
		cur = /^\d+$/.test(seg) && cur.items ? cur.items : cur.properties?.[seg];
	}
	return cur;
}

function fmtMin(ns: number): string {
	return `${Math.round(nsToMin(ns) * 100) / 100} min`;
}

/**
 * Validate a (validation-ready) value against the schema. Returns instancePath ->
 * first message ('' is the root). Required/additionalProperty errors are attributed
 * to the offending child path.
 */
export function validateSettings(schema: Schema, value: Json): FieldErrors {
	let fn = compiled.get(schema);
	if (!fn) {
		fn = ajv.compile(schema);
		compiled.set(schema, fn);
	}
	const out: FieldErrors = {};
	if (fn(value)) return out;
	for (const e of fn.errors ?? []) {
		let path = e.instancePath;
		let msg = e.message ?? 'Invalid';
		if (e.keyword === 'required') {
			path += `/${(e.params as { missingProperty: string }).missingProperty}`;
			msg = 'Required';
		} else if (e.keyword === 'additionalProperties') {
			path += `/${(e.params as { additionalProperty: string }).additionalProperty}`;
			msg = 'Unknown setting';
		} else if (e.keyword === 'enum') {
			msg = 'Must be one of the listed options';
		} else if (e.keyword === 'minimum' || e.keyword === 'maximum') {
			const s = schemaAt(schema, e.instancePath);
			const limit = (e.params as { limit: number }).limit;
			const word = e.keyword === 'minimum' ? 'at least' : 'at most';
			msg = `Must be ${word} ${s && isMinuteUnit(s) ? fmtMin(limit) : limit}`;
		}
		if (!(path in out)) out[path] = msg;
	}
	return out;
}

/** Convenience used by the page: validate the form state. */
export function validateForm(schema: Schema, form: Json, original: Json): FieldErrors {
	return validateSettings(schema, toValidationValue(schema, form, original));
}
