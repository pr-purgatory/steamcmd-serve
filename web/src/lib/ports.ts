import type { PortSpec } from './api/types';

export const MIN_PORT = 1024;
export const MAX_PORT = 65535;
export const MIN_MEMORY_MB = 128;

export interface PortRow {
	name: string;
	protocol: PortSpec['protocol'];
	derived: boolean;
	derivedFrom?: string;
	offset: number;
	description?: string;
	/** Resolved port number, or null when it cannot be computed yet. */
	port: number | null;
}

export type PortErrors = Record<string, string>;

/** Strict integer parse: digits only, no sign/space/decimal. */
export function parsePort(raw: string): number | null {
	const t = raw.trim();
	if (!/^\d{1,9}$/.test(t)) return null;
	return Number(t);
}

/** Initial input values: default for each non-derived port. */
export function defaultInputs(specs: PortSpec[]): Record<string, string> {
	const out: Record<string, string> = {};
	for (const s of specs) if (!s.derivedFrom) out[s.name] = String(s.default);
	return out;
}

/**
 * Resolve all rows (editable and derived). Derived = base + offset, where base
 * is the user's current input for the port named by `derivedFrom` (or its
 * resolved value if that port is itself derived).
 */
export function resolvePorts(specs: PortSpec[], inputs: Record<string, string>): PortRow[] {
	const byName = new Map(specs.map((s) => [s.name, s]));
	const resolve = (name: string, seen: Set<string>): number | null => {
		const s = byName.get(name);
		if (!s || seen.has(name)) return null;
		seen.add(name);
		if (!s.derivedFrom) return parsePort(inputs[name] ?? '');
		const base = resolve(s.derivedFrom, seen);
		return base === null ? null : base + (s.offset ?? 0);
	};
	return specs.map((s) => ({
		name: s.name,
		protocol: s.protocol,
		derived: !!s.derivedFrom,
		derivedFrom: s.derivedFrom,
		offset: s.offset ?? 0,
		description: s.description,
		port: resolve(s.name, new Set())
	}));
}

/**
 * Validate. Returns a map port name -> message; empty means valid.
 * Rules: required integer, 1024-65535 (derived ports included, reported on the
 * derived row), no two ports with the same port+protocol (reported on the later row).
 */
export function validatePorts(specs: PortSpec[], inputs: Record<string, string>): PortErrors {
	const errors: PortErrors = {};
	const rows = resolvePorts(specs, inputs);
	const seen = new Map<string, string>(); // "port/proto" -> first name

	for (const row of rows) {
		if (row.derived) {
			if (row.port === null) {
				errors[row.name] = `Depends on ${row.derivedFrom}, which is not a valid port yet`;
				continue;
			}
		} else {
			const raw = inputs[row.name] ?? '';
			if (raw.trim() === '') {
				errors[row.name] = 'Required';
				continue;
			}
			if (row.port === null) {
				errors[row.name] = 'Must be a whole number';
				continue;
			}
		}
		const port = row.port as number;
		if (port < MIN_PORT || port > MAX_PORT) {
			errors[row.name] = row.derived
				? `Derived port ${port} is outside ${MIN_PORT}-${MAX_PORT}`
				: `Must be between ${MIN_PORT} and ${MAX_PORT}`;
			continue;
		}
		const key = `${port}/${row.protocol}`;
		const first = seen.get(key);
		if (first !== undefined) {
			errors[row.name] = `${key} is already used by ${first}`;
		} else {
			seen.set(key, row.name);
		}
	}
	return errors;
}

export function validateMemory(raw: string): string | null {
	const t = raw.trim();
	if (!/^\d+$/.test(t)) return 'Must be a whole number';
	if (Number(t) < MIN_MEMORY_MB) return `Must be at least ${MIN_MEMORY_MB} MB`;
	return null;
}

export const NAME_PATTERN = /^[A-Za-z0-9][A-Za-z0-9 _.-]*$/;
export const NAME_MAX = 64;

export function validateName(raw: string): string | null {
	if (raw.length === 0) return 'Required';
	if (raw.length > NAME_MAX) return `At most ${NAME_MAX} characters`;
	if (!NAME_PATTERN.test(raw))
		return 'Start with a letter or digit; use letters, digits, spaces, _ . -';
	return null;
}

/** Body `ports` for InstanceCreate: only non-derived ports. */
export function createPortsPayload(specs: PortSpec[], inputs: Record<string, string>) {
	const out: Record<string, number> = {};
	for (const s of specs) {
		if (s.derivedFrom) continue;
		const p = parsePort(inputs[s.name] ?? '');
		if (p !== null) out[s.name] = p;
	}
	return out;
}

export function formatPort(p: { port: number; protocol: string }): string {
	return `${p.port}/${p.protocol}`;
}
