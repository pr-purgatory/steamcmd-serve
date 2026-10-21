import { describe, expect, it } from 'vitest';
import { humanSize } from './backups.ts';
import { canRestore } from './states.ts';

describe('backups helpers', () => {
	it('formats sizes', () => {
		expect(humanSize(0)).toBe('0 B');
		expect(humanSize(1536)).toBe('1.5 KiB');
		expect(humanSize(52_428_800)).toBe('50 MiB');
		expect(humanSize(-1)).toBe('-');
	});
	it('allows restore only when stopped or error', () => {
		expect(canRestore('stopped')).toBe(true);
		expect(canRestore('error')).toBe(true);
		for (const s of ['new', 'installing', 'starting', 'running', 'stopping', 'updating'] as const)
			expect(canRestore(s)).toBe(false);
	});
});
