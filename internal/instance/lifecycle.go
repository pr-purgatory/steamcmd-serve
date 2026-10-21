package instance

import (
	"context"
	"errors"
	"time"

	"steamcmd-serve/internal/catalog"
	"steamcmd-serve/internal/jobs"
)

// Actions accepted by Action (OpenAPI instanceAction).
const (
	ActionInstall = "install"
	ActionUpdate  = "update"
	ActionStart   = "start"
	ActionStop    = "stop"
	ActionRestart = "restart"
)

// LogEvent is one item of an instance's log stream.
type LogEvent struct {
	Kind string // "log" (Data = one line) or "state" (Data = new instance state)
	Data string
}

// Backup describes one backup archive (OpenAPI Backup).
type Backup struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"createdAt"`
	SizeBytes int64     `json:"sizeBytes"`
	Reason    string    `json:"reason"` // manual | scheduled | pre-update | pre-restore
}

var errNotImplemented = errors.New("not implemented")

// Games returns the game catalog.
func (s *Service) Games() map[string]*catalog.Game { return s.games }

// Action runs a lifecycle action in the background and returns its job.
// Invalid actions return ErrInvalid; actions not allowed in the current state ErrConflict.
func (s *Service) Action(ctx context.Context, id, action string) (*jobs.Job, error) {
	return nil, errNotImplemented
}

// Job returns a job snapshot (jobs.ErrNotFound if unknown).
func (s *Service) Job(id string) (*jobs.Job, error) { return s.jobs.Get(id) }

// StreamLogs sends the last tail lines of the server log, then follows it, calling emit
// for each line and for state changes, until ctx is done or emit returns an error.
func (s *Service) StreamLogs(ctx context.Context, id string, tail int, emit func(LogEvent) error) error {
	return errNotImplemented
}

// Backups lists an instance's backups, newest first.
func (s *Service) Backups(ctx context.Context, id string) ([]Backup, error) {
	return nil, errNotImplemented
}

// CreateBackup starts a manual backup job.
func (s *Service) CreateBackup(ctx context.Context, id string) (*jobs.Job, error) {
	return nil, errNotImplemented
}

// RestoreBackup starts a restore job (instance must be stopped; current data is backed up first).
func (s *Service) RestoreBackup(ctx context.Context, id, backupID string) (*jobs.Job, error) {
	return nil, errNotImplemented
}
