import type { components } from './api/schema';

export type Backup = components['schemas']['Backup'];

export const REASON_LABEL: Record<Backup['reason'], string> = {
	manual: 'Manual',
	scheduled: 'Scheduled',
	'pre-update': 'Before update',
	'pre-restore': 'Before restore'
};

export function humanSize(bytes: number): string {
	if (!Number.isFinite(bytes) || bytes < 0) return '-';
	const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
	let v = bytes;
	let u = 0;
	while (v >= 1024 && u < units.length - 1) {
		v /= 1024;
		u++;
	}
	return `${u === 0 ? v : v.toFixed(v >= 10 ? 0 : 1)} ${units[u]}`;
}
