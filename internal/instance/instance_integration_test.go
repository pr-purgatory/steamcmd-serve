package instance

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"steamcmd-serve/games"
	"steamcmd-serve/internal/catalog"
	"steamcmd-serve/internal/ports"
	"steamcmd-serve/internal/runtime"
	"steamcmd-serve/internal/store"
)

// Integration test against a real engine (Docker or Podman socket via DOCKER_HOST).
//
//	SS_IT_IMAGE=localhost/steamcmd-base:latest  image for the steamcmd-base runtime
//	SS_IT_DATA_DIR=/path                         dir the engine host can bind-mount (default ~/.cache/steamcmd-serve-it)
func TestServiceAgainstEngine(t *testing.T) {
	image := os.Getenv("SS_IT_IMAGE")
	if image == "" {
		t.Skip("set SS_IT_IMAGE (and DOCKER_HOST) to run against a container engine")
	}
	ctx := context.Background()
	dataDir := os.Getenv("SS_IT_DATA_DIR")
	if dataDir == "" {
		home, _ := os.UserHomeDir()
		dataDir = filepath.Join(home, ".cache", "steamcmd-serve-it")
	}
	dataDir = filepath.Join(dataDir, t.Name()+"-"+time.Now().Format("150405"))
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dataDir) })

	schema, _ := games.FS.ReadFile("manifest.schema.json")
	fsys := fstest.MapFS{
		"manifest.schema.json": {Data: schema},
		"sleeper/manifest.yaml": {Data: []byte(`
id: sleeper
name: Sleeper
steam: {serverAppId: 1, platform: linux}
runtime: {image: steamcmd-base}
launch: {command: ["sleep", "600"], stopSignal: SIGTERM, stopTimeoutSeconds: 5}
ports:
  - {name: game, default: 39100, protocol: udp}
  - {name: query, default: 39101, protocol: udp, derive: {from: game, offset: 1}}
settings: {schema: s.json}
persist: [{name: saves, path: saves, backup: true}]
readiness: {logRegex: "ready"}
resources: {memoryMB: 128, diskGB: 1}
mods: {loader: none}
`)},
		"sleeper/s.json": {Data: []byte(`{"type":"object","properties":{"name":{"type":"string","default":"x"}}}`)},
	}
	gs, err := catalog.Load(fsys)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dataDir, "controller.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rt, err := runtime.New()
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	if err := rt.Ping(ctx); err != nil {
		t.Fatalf("engine not reachable: %v", err)
	}
	opt := Options{DataDir: dataDir, HostDataRoot: dataDir, PUID: 1000, PGID: 1000, Images: map[string]string{"steamcmd-base": image}}
	svc, err := New(st, rt, gs, opt)
	if err != nil {
		t.Fatal(err)
	}

	a, _, err := svc.Create(ctx, CreateRequest{Name: "alpha", GameID: "sleeper"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rt.Remove(context.Background(), containerName(a.ID)) })
	b, _, err := svc.Create(ctx, CreateRequest{Name: "beta", GameID: "sleeper"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Ports[0].Port == b.Ports[0].Port || b.Ports[0].Port != 39102 {
		t.Fatalf("second instance should move past 39100+39101: a=%v b=%v", a.Ports, b.Ports)
	}
	if _, _, err := svc.Create(ctx, CreateRequest{Name: "gamma", GameID: "sleeper", Ports: map[string]int{"game": 39101}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("requested port overlapping alpha's derived port: want ErrConflict, got %v", err)
	}

	if err := svc.Start(ctx, a.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("start before install: want ErrConflict, got %v", err)
	}
	// Installation is T7; mark installed.
	if err := st.SetState(ctx, a.ID, store.StateStopped, ""); err != nil {
		t.Fatal(err)
	}
	if err := svc.Start(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if ok, _ := rt.Running(ctx, containerName(a.ID)); !ok {
		inst, _ := svc.Get(ctx, a.ID)
		t.Fatalf("container not running (state %s %s)", inst.State, inst.StateDetail)
	}
	published, err := rt.PublishedPorts(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range a.Ports {
		if !published[portKey(b.Port, b.Protocol)] {
			t.Errorf("port %d/%s not published: %v", b.Port, b.Protocol, published)
		}
	}
	if _, err := svc.Update(ctx, a.ID, UpdateRequest{Ports: map[string]int{"game": 39200}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("port change while running: want ErrConflict, got %v", err)
	}
	if err := svc.Delete(ctx, a.ID, false); !errors.Is(err, ErrConflict) {
		t.Fatalf("delete while running: want ErrConflict, got %v", err)
	}

	// Controller restart: state comes from the engine.
	if err := st.SetState(ctx, a.ID, store.StateStopped, ""); err != nil {
		t.Fatal(err)
	}
	svc2, _ := New(st, rt, gs, opt)
	if err := svc2.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if inst, _ := svc2.Get(ctx, a.ID); inst.State != store.StateRunning {
		t.Fatalf("after reconcile: state %s, want running", inst.State)
	}

	start := time.Now()
	if err := svc2.Stop(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("stop took %s; signal not forwarded?", d)
	}
	if inst, _ := svc2.Get(ctx, a.ID); inst.State != store.StateStopped {
		t.Fatalf("after stop: state %s", inst.State)
	}
	if fi, err := os.Stat(filepath.Join(dataDir, "instances", a.ID, "data", "saves")); err != nil || !fi.IsDir() {
		t.Fatalf("persist dir missing: %v", err)
	}
	if err := svc2.Delete(ctx, a.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "instances", a.ID)); !os.IsNotExist(err) {
		t.Fatalf("instance dir not deleted: %v", err)
	}
	if _, err := svc2.Get(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get after delete: %v", err)
	}
}

func portKey(port int, proto string) ports.Key { return ports.Key{Port: port, Protocol: proto} }
