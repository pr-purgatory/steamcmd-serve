import type { Job } from './api/types';

export class JobFailedError extends Error {
	readonly job: Job;
	constructor(job: Job) {
		super(job.message || 'Job failed');
		this.name = 'JobFailedError';
		this.job = job;
	}
}

export interface PollOptions {
	intervalMs?: number;
	signal?: AbortSignal;
	/** Called with every fetched job state (including the final one). */
	onUpdate?: (job: Job) => void;
}

function abortError(): Error {
	return new DOMException('Polling aborted', 'AbortError');
}

function sleep(ms: number, signal?: AbortSignal): Promise<void> {
	return new Promise((resolve, reject) => {
		if (signal?.aborted) return reject(abortError());
		const onAbort = () => {
			clearTimeout(t);
			reject(abortError());
		};
		const t = setTimeout(() => {
			signal?.removeEventListener('abort', onAbort);
			resolve();
		}, ms);
		signal?.addEventListener('abort', onAbort, { once: true });
	});
}

/**
 * Polls `fetchJob` (immediately, then every `intervalMs`) until the job succeeds (resolves
 * with it) or fails (rejects with JobFailedError). Aborting rejects with an AbortError and
 * stops all further fetches. Errors thrown by `fetchJob` reject too.
 */
export async function pollJob(
	fetchJob: () => Promise<Job>,
	opts: PollOptions = {}
): Promise<Job> {
	const { intervalMs = 1000, signal, onUpdate } = opts;
	for (;;) {
		if (signal?.aborted) throw abortError();
		const job = await fetchJob();
		if (signal?.aborted) throw abortError();
		onUpdate?.(job);
		if (job.status === 'succeeded') return job;
		if (job.status === 'failed') throw new JobFailedError(job);
		await sleep(intervalMs, signal);
	}
}

export function isAbortError(e: unknown): boolean {
	return e instanceof Error && e.name === 'AbortError';
}
