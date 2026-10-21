import type { InstanceState } from './api/types';

export const STATE_LABEL: Record<InstanceState, string> = {
	new: 'New (not installed)',
	installing: 'Installing',
	stopped: 'Stopped',
	starting: 'Starting',
	running: 'Running',
	stopping: 'Stopping',
	updating: 'Updating',
	error: 'Error'
};

/** CSS modifier class per state; paired with a text label and glyph, never color alone. */
export const STATE_CLASS: Record<InstanceState, string> = {
	new: 'st-new',
	installing: 'st-busy',
	stopped: 'st-stopped',
	starting: 'st-busy',
	running: 'st-ok',
	stopping: 'st-busy',
	updating: 'st-busy',
	error: 'st-err'
};

export const STATE_GLYPH: Record<InstanceState, string> = {
	new: '○',
	installing: '⇣',
	stopped: '■',
	starting: '◔',
	running: '▶',
	stopping: '◑',
	updating: '⟳',
	error: '✖'
};

export const canStart = (s: InstanceState) => s === 'stopped';
export const canStop = (s: InstanceState) => s === 'running' || s === 'starting';
export const canRestart = (s: InstanceState) => s === 'running';
export const canInstall = (s: InstanceState) => s === 'new' || s === 'error';
export const canUpdate = (s: InstanceState) => s === 'stopped';
/** Restoring a backup needs the server stopped (or crashed). */
export const canRestore = (s: InstanceState) => s === 'stopped' || s === 'error';
