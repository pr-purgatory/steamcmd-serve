import { describe, expect, it } from 'vitest';
import type { InstanceState } from './api/types';
import { canEditInstance, canInstall, canRestart, canStart, canStop, canUpdate } from './states';

const ALL: InstanceState[] = ['new', 'installing', 'stopped', 'starting', 'running', 'stopping', 'updating', 'error'];
const allowed = (f: (s: InstanceState) => boolean) => ALL.filter(f);

describe('action enablement matches server rules', () => {
	it('install: new, error', () => expect(allowed(canInstall)).toEqual(['new', 'error']));
	it('update: stopped, error', () => expect(allowed(canUpdate)).toEqual(['stopped', 'error']));
	it('start: stopped, error', () => expect(allowed(canStart)).toEqual(['stopped', 'error']));
	it('stop: starting, running', () => expect(allowed(canStop)).toEqual(['starting', 'running']));
	it('restart: running', () => expect(allowed(canRestart)).toEqual(['running']));
	it('edit/delete: new, stopped, error', () =>
		expect(allowed(canEditInstance)).toEqual(['new', 'stopped', 'error']));
});
