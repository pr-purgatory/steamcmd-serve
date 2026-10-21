package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"steamcmd-serve/internal/ports"
)

func open(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func inst(id, name string, port int) *Instance {
	return &Instance{
		ID: id, Name: name, GameID: "enshrouded", MemoryMB: 8192,
		Settings: []byte(`{"name":"x"}`), State: StateNew, CreatedAt: time.Now(),
		Ports: []ports.Binding{{Name: "query", Port: port, Protocol: "udp"}},
	}
}

func TestInstanceLifecycle(t *testing.T) {
	ctx := context.Background()
	s, path := open(t)

	if err := s.CreateInstance(ctx, inst("a", "Alpha", 15637)); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateInstance(ctx, inst("b", "beta", 15638)); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateInstance(ctx, inst("c", "gamma", 15637)); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate port: want ErrConflict, got %v", err)
	}
	if err := s.CreateInstance(ctx, inst("d", "ALPHA", 15640)); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate name (case-insensitive): want ErrConflict, got %v", err)
	}
	if _, err := s.GetInstance(ctx, "c"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("failed create must roll back, got %v", err)
	}

	taken, err := s.TakenPorts(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	if len(taken) != 1 || !taken[ports.Key{Port: 15638, Protocol: "udp"}] {
		t.Fatalf("TakenPorts excluding a = %v", taken)
	}

	b, _ := s.GetInstance(ctx, "b")
	b.Ports[0].Port = 15637
	if err := s.UpdateInstance(ctx, b); !errors.Is(err, ErrConflict) {
		t.Fatalf("update to taken port: want ErrConflict, got %v", err)
	}
	if got, _ := s.GetInstance(ctx, "b"); got.Ports[0].Port != 15638 {
		t.Fatalf("failed update must roll back, port = %d", got.Ports[0].Port)
	}

	if err := s.Transition(ctx, "a", []string{StateStopped, StateError}, StateStarting, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("transition from wrong state: want ErrConflict, got %v", err)
	}
	if err := s.Transition(ctx, "zzz", []string{StateNew}, StateInstalling, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("transition unknown: want ErrNotFound, got %v", err)
	}
	if err := s.Transition(ctx, "a", []string{StateNew, StateError}, StateInstalling, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SetState(ctx, "a", StateStopped, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteInstance(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	list, _ := s.ListInstances(ctx)
	if len(list) != 1 || list[0].State != StateStopped || len(list[0].Ports) != 1 {
		t.Fatalf("list = %+v", list)
	}
	if taken, _ := s.TakenPorts(ctx, ""); taken[ports.Key{Port: 15638, Protocol: "udp"}] {
		t.Fatal("ports not cascaded on delete")
	}

	// Reopen: migrations are idempotent and data persists.
	s.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if got, err := s2.GetInstance(ctx, "a"); err != nil || got.Name != "Alpha" {
		t.Fatalf("reopen: %v %v", got, err)
	}
}
