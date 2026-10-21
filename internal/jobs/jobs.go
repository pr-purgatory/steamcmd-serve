// Package jobs tracks long-running operations (install, update, backup, ...).
package jobs

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Job statuses (OpenAPI Job.status).
const (
	Queued    = "queued"
	Running   = "running"
	Succeeded = "succeeded"
	Failed    = "failed"
)

// ErrNotFound is returned for unknown job ids.
var ErrNotFound = errors.New("not found")

// Job is a snapshot of an operation (OpenAPI Job).
type Job struct {
	ID         string     `json:"id"`
	InstanceID string     `json:"instanceId,omitempty"`
	Kind       string     `json:"kind"`
	Status     string     `json:"status"`
	Progress   float64    `json:"progress"`
	Message    string     `json:"message,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

// Report lets a running job publish progress (0..1) and a status line.
type Report func(progress float64, message string)

// Tracker runs jobs in the background and keeps recent ones in memory.
// Jobs are not persisted: after a controller restart, Reconcile repairs instance state.
type Tracker struct {
	mu   sync.Mutex
	jobs map[string]*Job
	wg   sync.WaitGroup
	ctx  context.Context
}

// NewTracker returns a Tracker whose jobs are cancelled when ctx is done.
func NewTracker(ctx context.Context) *Tracker {
	return &Tracker{jobs: map[string]*Job{}, ctx: ctx}
}

// Start runs fn in the background and returns its job snapshot.
func (t *Tracker) Start(instanceID, kind string, fn func(ctx context.Context, report Report) error) *Job {
	j := &Job{ID: strings.ToLower(rand.Text()[:16]), InstanceID: instanceID, Kind: kind, Status: Running, CreatedAt: time.Now().UTC()}
	t.mu.Lock()
	t.jobs[j.ID] = j
	t.prune()
	snap := *j
	t.mu.Unlock()

	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		err := fn(t.ctx, func(p float64, msg string) {
			t.mu.Lock()
			j.Progress, j.Message = p, msg
			t.mu.Unlock()
		})
		now := time.Now().UTC()
		t.mu.Lock()
		j.FinishedAt = &now
		if err != nil {
			j.Status, j.Message = Failed, err.Error()
			slog.Warn("job failed", "job", j.ID, "kind", kind, "instance", instanceID, "err", err)
		} else {
			j.Status, j.Progress = Succeeded, 1
		}
		t.mu.Unlock()
	}()
	return &snap
}

// Get returns a snapshot of a job.
func (t *Tracker) Get(id string) (*Job, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	j, ok := t.jobs[id]
	if !ok {
		return nil, ErrNotFound
	}
	snap := *j
	return &snap, nil
}

// Wait blocks until all running jobs return.
func (t *Tracker) Wait() { t.wg.Wait() }

// prune drops finished jobs older than an hour. Caller holds t.mu.
func (t *Tracker) prune() {
	cutoff := time.Now().Add(-time.Hour)
	for id, j := range t.jobs {
		if j.FinishedAt != nil && j.FinishedAt.Before(cutoff) {
			delete(t.jobs, id)
		}
	}
}
