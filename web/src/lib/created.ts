/** Hands create-time preflight warnings to the new instance's detail page (tab-scoped). */
const key = (id: string) => `scs-created-warnings:${id}`;

export function setCreatedWarnings(id: string, warnings: string[]): void {
	try {
		if (warnings.length) sessionStorage.setItem(key(id), JSON.stringify(warnings));
		else sessionStorage.removeItem(key(id));
	} catch {
		/* storage unavailable: warnings are best-effort */
	}
}

/** Reads and clears the stored warnings. */
export function takeCreatedWarnings(id: string): string[] {
	try {
		const raw = sessionStorage.getItem(key(id));
		sessionStorage.removeItem(key(id));
		const v: unknown = raw ? JSON.parse(raw) : [];
		return Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : [];
	} catch {
		return [];
	}
}
