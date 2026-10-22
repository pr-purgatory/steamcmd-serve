import type { Client } from 'openapi-fetch';
import type { paths } from './api/schema';
import type { InstalledMod, InstanceState, ModProvider, ModRef, NexusAccount } from './api/types';

/** Largest archive the server accepts for a manual upload. */
export const MAX_UPLOAD_BYTES = 1024 ** 3;

/** Mod changes need the server stopped (or crashed). */
export const canChangeMods = (s: InstanceState) => s === 'stopped' || s === 'error';

/** Package identity: Nexus refs are "<modId>:<fileId>" once installed, search returns "<modId>". */
const packageKey = (id: string) => id.split(':', 1)[0];

/** Installed entry for the same provider + package, if any. */
export function findInstalled(
	list: readonly InstalledMod[],
	provider: ModProvider,
	refId: string
): InstalledMod | undefined {
	return list.find((m) => m.provider === provider && packageKey(m.ref.id) === packageKey(refId));
}

/** "dependency of X, Y" label; ids that are not installed are shown as-is. */
export function requiredByLabel(mod: InstalledMod, all: readonly InstalledMod[]): string {
	const names = mod.requiredBy.map((id) => all.find((m) => m.id === id)?.ref.id ?? id);
	return names.length ? `dependency of ${names.join(', ')}` : 'dependency';
}

/** True when a failed install job message says NexusMods needs a manual download. */
export function isManualDownloadFailure(message: string | undefined | null): boolean {
	return !!message && /manual download/i.test(message);
}

/** Why the upload cannot be sent, or null when it is fine. */
export function uploadProblem(file: { name: string; size: number } | null | undefined): string | null {
	if (!file) return 'Choose the .zip file you downloaded.';
	if (!/\.zip$/i.test(file.name)) return 'Only .zip archives are supported.';
	if (file.size > MAX_UPLOAD_BYTES) return 'The file is larger than 1 GiB.';
	if (file.size === 0) return 'The file is empty.';
	return null;
}

/** Multipart body for a manual upload. Field order matters: provider, ref, then file last. */
export function buildUploadForm(provider: ModProvider, ref: ModRef, file: Blob, filename?: string): FormData {
	const form = new FormData();
	form.append('provider', provider);
	form.append('ref', JSON.stringify({ id: ref.id, version: ref.version }));
	if (filename !== undefined) form.append('file', file, filename);
	else form.append('file', file);
	return form;
}

export interface Debounced<A extends unknown[]> {
	(...args: A): void;
	cancel(): void;
}

/** Trailing-edge debounce; `cancel()` drops a pending call. */
export function debounce<A extends unknown[]>(fn: (...args: A) => void, ms: number): Debounced<A> {
	let t: ReturnType<typeof setTimeout> | undefined;
	const d = ((...args: A) => {
		if (t !== undefined) clearTimeout(t);
		t = setTimeout(() => {
			t = undefined;
			fn(...args);
		}, ms);
	}) as Debounced<A>;
	d.cancel = () => {
		if (t !== undefined) clearTimeout(t);
		t = undefined;
	};
	return d;
}

/** Search text the server should see: null means "too short, do not search yet". */
export function normalizeQuery(q: string): string | null {
	const t = q.trim();
	if (t.length === 0) return '';
	return t.length >= 2 ? t : null;
}

/** Human status line for the NexusMods account. */
export function nexusStatus(acc: NexusAccount | null): string {
	if (!acc || !acc.hasValue) return 'Not set';
	const who = acc.username ? `Connected as ${acc.username}` : 'Connected';
	return acc.premium === undefined ? who : `${who} (${acc.premium ? 'Premium' : 'Free'})`;
}

/**
 * Stores the NexusMods key. The key travels only in the JSON body, never in the URL
 * or query string, and is not retained anywhere by this helper.
 */
export function putNexusKey(client: Client<paths>, apiKey: string) {
	return client.PUT('/settings/nexus-api-key', { body: { apiKey } });
}
