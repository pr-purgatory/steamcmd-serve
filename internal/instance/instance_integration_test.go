package instance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"steamcmd-serve/games"
	"steamcmd-serve/internal/catalog"
	"steamcmd-serve/internal/jobs"
	"steamcmd-serve/internal/ports"
	"steamcmd-serve/internal/runtime"
	"steamcmd-serve/internal/secrets"
	"steamcmd-serve/internal/store"
)

// Integration test against a real engine (Docker or Podman socket via DOCKER_HOST).
//
//	SS_IT_IMAGE     fake runtime image (make it-image)
//	SS_IT_DATA_DIR  dir the engine host can bind-mount (default ~/.cache/steamcmd-serve-it)
func TestIntegrationLifecycle(t *testing.T) {
	image := os.Getenv("SS_IT_IMAGE")
	if image == "" {
		t.Skip("set SS_IT_IMAGE (make it-image) and DOCKER_HOST to run against a container engine")
	}
	ctx := context.Background()
	dataDir := os.Getenv("SS_IT_DATA_DIR")
	if dataDir == "" {
		home, _ := os.UserHomeDir()
		dataDir = filepath.Join(home, ".cache", "steamcmd-serve-it")
	}
	dataDir = filepath.Join(dataDir, time.Now().Format("150405.000"))
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dataDir) })

	manifest := func(id string, appID string) string {
		return `
id: ` + id + `
name: Fake
steam: {serverAppId: ` + appID + `, platform: linux}
runtime: {image: steamcmd-base}
launch: {command: ["sh", "-c", "echo booting; echo {{ .Settings.motd }}; echo READY on {{ .Ports.game }}; trap 'echo bye; exit 0' TERM; sleep 600 & wait"], stopSignal: SIGTERM, stopTimeoutSeconds: 5}
ports:
  - {name: game, default: 39100, protocol: udp}
  - {name: query, default: 39101, protocol: udp, derive: {from: game, offset: 1}}
settings: {schema: s.json, file: {path: cfg/server.json, format: json, template: t.tmpl}}
persist: [{name: saves, path: saves, backup: true}]
readiness: {logRegex: "^READY on \\d+$", timeoutSeconds: 30}
resources: {memoryMB: 128, diskGB: 1}
mods: {loader: none}
`
	}
	schema, _ := games.FS.ReadFile("manifest.schema.json")
	settingsSchema := []byte(`{"type":"object","properties":{"motd":{"type":"string","default":"hello"},"rcon":{"type":"string","x-secret":true,"x-generate":"password"}}}`)
	tmpl := []byte(`{"motd": {{ json .Settings.motd }}, "port": {{ .Ports.game }}, "rcon": {{ json .Settings.rcon }}}`)
	fsys := fstest.MapFS{
		"manifest.schema.json": {Data: schema},
		"fake/manifest.yaml":   {Data: []byte(manifest("fake", "1"))},
		"fake/s.json":          {Data: settingsSchema},
		"fake/t.tmpl":          {Data: tmpl},
		"broken/manifest.yaml": {Data: []byte(strings.NewReplacer("39100", "39300", "39101", "39301").Replace(manifest("broken", "666")))},
		"broken/s.json":        {Data: settingsSchema},
		"broken/t.tmpl":        {Data: tmpl},
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
	box, err := secrets.Open(filepath.Join(dataDir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	gamesDir := filepath.Join(dataDir, "separate-games-share")
	opt := Options{DataDir: dataDir, HostDataRoot: dataDir, GamesDir: gamesDir, HostGamesRoot: gamesDir, PUID: 1000, PGID: 1000, Images: map[string]string{"steamcmd-base": image}, Secrets: box}
	jt := jobs.NewTracker(ctx)
	svc, err := New(st, rt, gs, jt, opt)
	if err != nil {
		t.Fatal(err)
	}

	wait := func(j *jobs.Job, err error) *jobs.Job {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(60 * time.Second)
		for time.Now().Before(deadline) {
			cur, _ := svc.Job(j.ID)
			if cur.Status == jobs.Succeeded || cur.Status == jobs.Failed {
				return cur
			}
			time.Sleep(200 * time.Millisecond)
		}
		t.Fatalf("job %s (%s) did not finish", j.ID, j.Kind)
		return nil
	}
	state := func(id string) *store.Instance {
		t.Helper()
		inst, err := svc.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return inst
	}

	// --- create & ports ---
	a, _, err := svc.Create(ctx, CreateRequest{Name: "alpha", GameID: "fake"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rt.Remove(context.Background(), containerName(a.ID)) })
	b, _, err := svc.Create(ctx, CreateRequest{Name: "beta", GameID: "fake"})
	if err != nil {
		t.Fatal(err)
	}
	if b.Ports[0].Port != 39102 {
		t.Fatalf("second instance should move past 39100+39101: %v", b.Ports)
	}
	if _, _, err := svc.Create(ctx, CreateRequest{Name: "gamma", GameID: "fake", Ports: map[string]int{"game": 39101}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("overlapping derived port: want ErrConflict, got %v", err)
	}

	// --- state guards ---
	if _, err := svc.Action(ctx, a.ID, ActionStart); !errors.Is(err, ErrConflict) {
		t.Fatalf("start before install: want ErrConflict, got %v", err)
	}
	if _, err := svc.Action(ctx, a.ID, "explode"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown action: want ErrInvalid, got %v", err)
	}

	// --- install (job container) ---
	j := wait(svc.Action(ctx, a.ID, ActionInstall))
	if j.Status != jobs.Succeeded || state(a.ID).State != store.StateStopped {
		t.Fatalf("install: job %+v state %s", j, state(a.ID).State)
	}
	if _, err := os.Stat(filepath.Join(gamesDir, "fake", a.ID, "fake_server")); err != nil {
		t.Fatalf("install did not write into the instance server dir: %v", err)
	}

	broken, _, err := svc.Create(ctx, CreateRequest{Name: "broken", GameID: "broken"})
	if err != nil {
		t.Fatal(err)
	}
	j = wait(svc.Action(ctx, broken.ID, ActionInstall))
	if inst := state(broken.ID); j.Status != jobs.Failed || inst.State != store.StateError || !strings.Contains(inst.StateDetail, "No subscription") {
		t.Fatalf("failed install: job %+v state %s detail %q", j, inst.State, inst.StateDetail)
	}

	// --- settings → rendered config, start waits for readiness ---
	if _, err := svc.SetSettings(ctx, a.ID, catalog.Settings{"motd": "welcome", "rcon": map[string]any{"hasValue": true}}); err != nil {
		t.Fatal(err)
	}
	j, err = svc.Action(ctx, a.ID, ActionStart)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Action(ctx, a.ID, ActionStart); !errors.Is(err, ErrConflict) {
		t.Fatalf("double start: want ErrConflict, got %v", err)
	}
	if j = wait(j, nil); j.Status != jobs.Succeeded {
		t.Fatalf("start job: %+v", j)
	}
	if inst := state(a.ID); inst.State != store.StateRunning || inst.StateDetail != "" {
		t.Fatalf("after start: %s %q (want running, readiness seen)", inst.State, inst.StateDetail)
	}
	cfgRaw, err := os.ReadFile(filepath.Join(gamesDir, "fake", a.ID, "cfg", "server.json"))
	var cfg map[string]any
	if err != nil || json.Unmarshal(cfgRaw, &cfg) != nil || cfg["motd"] != "welcome" || cfg["port"] != 39100.0 {
		t.Fatalf("rendered config = %q, %v", cfgRaw, err)
	}
	rcon, _ := cfg["rcon"].(string)
	if len(rcon) < 16 || strings.HasPrefix(rcon, "enc:") {
		t.Fatalf("rendered secret must be the decrypted generated value, got %q", rcon)
	}
	if raw, _ := st.GetInstance(ctx, a.ID); bytes.Contains(raw.Settings, []byte(rcon)) || !bytes.Contains(raw.Settings, []byte("enc:v1:")) {
		t.Fatal("secret stored in plaintext")
	}
	if masked, _ := svc.Settings(ctx, a.ID); fmt.Sprint(masked["rcon"]) != "map[hasValue:true]" {
		t.Fatalf("API settings must mask secrets, got %v", masked["rcon"])
	}
	published, _ := rt.PublishedPorts(ctx, "")
	for _, p := range a.Ports {
		if !published[ports.Key{Port: p.Port, Protocol: p.Protocol}] {
			t.Errorf("port %d/%s not published", p.Port, p.Protocol)
		}
	}

	// --- logs stream ---
	lctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	var lines []string
	svc.StreamLogs(lctx, a.ID, 100, func(e LogEvent) error {
		if e.Kind == "log" {
			lines = append(lines, e.Data)
			if strings.HasPrefix(e.Data, "READY") {
				cancel()
			}
		}
		return nil
	})
	cancel()
	if strings.Join(lines, "\n") != "booting\nwelcome\nREADY on 39100" {
		t.Fatalf("log lines = %q", lines)
	}

	// --- guards while running ---
	if _, err := svc.Update(ctx, a.ID, UpdateRequest{Ports: map[string]int{"game": 39200}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("port change while running: want ErrConflict, got %v", err)
	}
	if err := svc.Delete(ctx, a.ID, false); !errors.Is(err, ErrConflict) {
		t.Fatalf("delete while running: want ErrConflict, got %v", err)
	}

	// --- game rewrites its config while running → drift reported on stop ---
	if err := os.WriteFile(filepath.Join(gamesDir, "fake", a.ID, "cfg", "server.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// --- backup while running ---
	if err := os.WriteFile(filepath.Join(dataDir, "instances", a.ID, "data", "saves", "world.sav"), []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if j := wait(svc.CreateBackup(ctx, a.ID)); j.Status != jobs.Succeeded {
		t.Fatalf("backup: %+v", j)
	}

	// --- controller restart: boot reconcile keeps it running ---
	svc2, _ := New(st, rt, gs, jt, opt)
	if err := svc2.Reconcile(ctx, true); err != nil {
		t.Fatal(err)
	}
	if inst := state(a.ID); inst.State != store.StateRunning {
		t.Fatalf("after reconcile: %s", inst.State)
	}

	// --- stop ---
	start := time.Now()
	if j := wait(svc.Action(ctx, a.ID, ActionStop)); j.Status != jobs.Succeeded {
		t.Fatalf("stop: %+v", j)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("stop took %s; signal not handled?", d)
	}
	if inst := state(a.ID); inst.State != store.StateStopped || !strings.Contains(inst.StateDetail, "changed cfg/server.json") {
		t.Fatalf("after stop: %s %q (want drift note)", inst.State, inst.StateDetail)
	}

	// --- restore ---
	if err := os.WriteFile(filepath.Join(dataDir, "instances", a.ID, "data", "saves", "world.sav"), []byte("v2-broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	backups, err := svc.Backups(ctx, a.ID)
	if err != nil || len(backups) != 1 || backups[0].Reason != ReasonManual {
		t.Fatalf("backups = %+v, %v", backups, err)
	}
	if _, err := svc.RestoreBackup(ctx, a.ID, "../../etc"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("restore with bad id: want ErrNotFound, got %v", err)
	}
	if j := wait(svc.RestoreBackup(ctx, a.ID, backups[0].ID)); j.Status != jobs.Succeeded {
		t.Fatalf("restore: %+v", j)
	}
	if got, _ := os.ReadFile(filepath.Join(dataDir, "instances", a.ID, "data", "saves", "world.sav")); string(got) != "v1" {
		t.Fatalf("restored save = %q", got)
	}
	if backups, _ := svc.Backups(ctx, a.ID); len(backups) != 2 || backups[0].Reason != ReasonPreRestore {
		t.Fatalf("pre-restore backup missing: %+v", backups)
	}

	// --- crash detection by periodic reconcile ---
	wait(svc.Action(ctx, a.ID, ActionStart))
	if err := rt.Stop(ctx, containerName(a.ID)); err != nil {
		t.Fatal(err)
	}
	if err := svc.Reconcile(ctx, false); err != nil {
		t.Fatal(err)
	}
	if inst := state(a.ID); inst.State != store.StateError {
		t.Fatalf("container stopped outside the controller: state %s, want error", inst.State)
	}

	// --- delete with data ---
	if err := svc.Delete(ctx, a.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "instances", a.ID)); !os.IsNotExist(err) {
		t.Fatalf("instance dir not deleted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(gamesDir, "fake", a.ID)); !os.IsNotExist(err) {
		t.Fatalf("install dir not deleted: %v", err)
	}
}
