// Package store persists controller state in SQLite.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"steamcmd-serve/internal/ports"

	_ "modernc.org/sqlite"
)

// Errors returned by Store.
var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
)

// Instance states (OpenAPI InstanceState).
const (
	StateNew        = "new"
	StateInstalling = "installing"
	StateStopped    = "stopped"
	StateStarting   = "starting"
	StateRunning    = "running"
	StateStopping   = "stopping"
	StateUpdating   = "updating"
	StateError      = "error"
)

// Instance is one game-server instance.
type Instance struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	GameID      string          `json:"gameId"`
	MemoryMB    int             `json:"memoryMB"`
	Settings    json.RawMessage `json:"-"`
	State       string          `json:"state"`
	StateDetail string          `json:"stateDetail,omitempty"`
	CreatedAt   time.Time       `json:"createdAt"`
	Ports       []ports.Binding `json:"ports"`
}

// Store wraps the SQLite database.
type Store struct{ db *sql.DB }

var migrations = []string{
	`CREATE TABLE instances (
		id           TEXT PRIMARY KEY,
		name         TEXT NOT NULL UNIQUE COLLATE NOCASE,
		game_id      TEXT NOT NULL,
		memory_mb    INTEGER NOT NULL,
		settings     TEXT NOT NULL,
		state        TEXT NOT NULL,
		state_detail TEXT NOT NULL DEFAULT '',
		created_at   TEXT NOT NULL
	);
	CREATE TABLE instance_ports (
		instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
		name        TEXT NOT NULL,
		port        INTEGER NOT NULL,
		protocol    TEXT NOT NULL,
		PRIMARY KEY (instance_id, name),
		UNIQUE (port, protocol)
	);`,
}

// Open opens (creating if needed) and migrates the database at path.
func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // one writer; avoids SQLITE_BUSY between our own goroutines
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	var v int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	for i := v; i < len(migrations); i++ {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// CreateInstance inserts inst and its ports. Duplicate names or ports return ErrConflict.
func (s *Store) CreateInstance(ctx context.Context, inst *Instance) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO instances (id, name, game_id, memory_mb, settings, state, state_detail, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			inst.ID, inst.Name, inst.GameID, inst.MemoryMB, string(inst.Settings), inst.State, inst.StateDetail, inst.CreatedAt.UTC().Format(time.RFC3339Nano))
		if err != nil {
			return conflict(err)
		}
		return insertPorts(ctx, tx, inst.ID, inst.Ports)
	})
}

// UpdateInstance saves name, memory, settings and ports of inst.
func (s *Store) UpdateInstance(ctx context.Context, inst *Instance) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE instances SET name = ?, memory_mb = ?, settings = ? WHERE id = ?`,
			inst.Name, inst.MemoryMB, string(inst.Settings), inst.ID)
		if err != nil {
			return conflict(err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM instance_ports WHERE instance_id = ?`, inst.ID); err != nil {
			return err
		}
		return insertPorts(ctx, tx, inst.ID, inst.Ports)
	})
}

// SetState records an instance's lifecycle state.
func (s *Store) SetState(ctx context.Context, id, state, detail string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE instances SET state = ?, state_detail = ? WHERE id = ?`, state, detail, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteInstance removes the instance and its ports.
func (s *Store) DeleteInstance(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM instances WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetInstance loads one instance.
func (s *Store) GetInstance(ctx context.Context, id string) (*Instance, error) {
	list, err := s.query(ctx, `WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, ErrNotFound
	}
	return list[0], nil
}

// ListInstances loads all instances ordered by name.
func (s *Store) ListInstances(ctx context.Context) ([]*Instance, error) {
	return s.query(ctx, `ORDER BY name COLLATE NOCASE`)
}

// TakenPorts returns ports assigned to instances other than excludeID.
func (s *Store) TakenPorts(ctx context.Context, excludeID string) (map[ports.Key]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT port, protocol FROM instance_ports WHERE instance_id != ?`, excludeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	taken := map[ports.Key]bool{}
	for rows.Next() {
		var k ports.Key
		if err := rows.Scan(&k.Port, &k.Protocol); err != nil {
			return nil, err
		}
		taken[k] = true
	}
	return taken, rows.Err()
}

func (s *Store) query(ctx context.Context, where string, args ...any) ([]*Instance, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, game_id, memory_mb, settings, state, state_detail, created_at FROM instances `+where, args...)
	if err != nil {
		return nil, err
	}
	var list []*Instance
	byID := map[string]*Instance{}
	for rows.Next() {
		var inst Instance
		var settings, created string
		if err := rows.Scan(&inst.ID, &inst.Name, &inst.GameID, &inst.MemoryMB, &settings, &inst.State, &inst.StateDetail, &created); err != nil {
			rows.Close()
			return nil, err
		}
		inst.Settings = json.RawMessage(settings)
		if inst.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
			rows.Close()
			return nil, err
		}
		inst.Ports = []ports.Binding{}
		list = append(list, &inst)
		byID[inst.ID] = &inst
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(list) == 0 {
		return list, err
	}

	prow, err := s.db.QueryContext(ctx, `SELECT instance_id, name, port, protocol FROM instance_ports ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer prow.Close()
	for prow.Next() {
		var id string
		var b ports.Binding
		if err := prow.Scan(&id, &b.Name, &b.Port, &b.Protocol); err != nil {
			return nil, err
		}
		if inst := byID[id]; inst != nil {
			inst.Ports = append(inst.Ports, b)
		}
	}
	return list, prow.Err()
}

func insertPorts(ctx context.Context, tx *sql.Tx, id string, bs []ports.Binding) error {
	for _, b := range bs {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO instance_ports (instance_id, name, port, protocol) VALUES (?, ?, ?, ?)`,
			id, b.Name, b.Port, b.Protocol); err != nil {
			return conflict(err)
		}
	}
	return nil
}

func (s *Store) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// conflict maps SQLite UNIQUE violations to ErrConflict.
func conflict(err error) error {
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return fmt.Errorf("%w: %v", ErrConflict, err)
	}
	return err
}
