import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { JobFailedError, isAbortError, pollJob } from './jobs.ts';
import type { Job } from './api/types';

const job = (status: Job['status'], extra: Partial<Job> = {}): Job => ({
	id: 'j1',
	kind: 'backup',
	status,
	createdAt: '2026-10-01T00:00:00Z',
	...extra
});

describe('pollJob', () => {
	beforeEach(() => vi.useFakeTimers());
	afterEach(() => vi.useRealTimers());

	it('polls every second and resolves on succeeded', async () => {
		const seq = [job('queued'), job('running', { progress: 0.5 }), job('succeeded', { progress: 1 })];
		const fetchJob = vi.fn(async () => seq.shift() as Job);
		const updates: string[] = [];
		const p = pollJob(fetchJob, { onUpdate: (j) => updates.push(j.status) });
		await vi.advanceTimersByTimeAsync(0);
		expect(fetchJob).toHaveBeenCalledTimes(1);
		await vi.advanceTimersByTimeAsync(999);
		expect(fetchJob).toHaveBeenCalledTimes(1);
		await vi.advanceTimersByTimeAsync(1);
		expect(fetchJob).toHaveBeenCalledTimes(2);
		await vi.advanceTimersByTimeAsync(1000);
		await expect(p).resolves.toMatchObject({ status: 'succeeded' });
		expect(updates).toEqual(['queued', 'running', 'succeeded']);
		await vi.advanceTimersByTimeAsync(5000);
		expect(fetchJob).toHaveBeenCalledTimes(3);
	});

	it('rejects with JobFailedError carrying the message on failed', async () => {
		const fetchJob = vi.fn(async () => job('failed', { message: 'disk full' }));
		const p = pollJob(fetchJob);
		const assertion = expect(p).rejects.toThrow('disk full');
		await vi.advanceTimersByTimeAsync(0);
		await assertion;
		await expect(pollJob(fetchJob)).rejects.toBeInstanceOf(JobFailedError);
	});

	it('stops on abort and does not fetch again', async () => {
		const fetchJob = vi.fn(async () => job('running'));
		const ac = new AbortController();
		const p = pollJob(fetchJob, { signal: ac.signal });
		const assertion = p.catch((e: unknown) => e);
		await vi.advanceTimersByTimeAsync(1500);
		expect(fetchJob).toHaveBeenCalledTimes(2);
		ac.abort();
		const err = await assertion;
		expect(isAbortError(err)).toBe(true);
		await vi.advanceTimersByTimeAsync(10000);
		expect(fetchJob).toHaveBeenCalledTimes(2);
	});

	it('rejects immediately when already aborted', async () => {
		const fetchJob = vi.fn(async () => job('running'));
		const ac = new AbortController();
		ac.abort();
		await expect(pollJob(fetchJob, { signal: ac.signal })).rejects.toSatisfy(isAbortError);
		expect(fetchJob).not.toHaveBeenCalled();
	});
});
