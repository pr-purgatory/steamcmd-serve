package instance

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"steamcmd-serve/games"
	"steamcmd-serve/internal/catalog"
	"steamcmd-serve/internal/jobs"
	"steamcmd-serve/internal/mods"
	"steamcmd-serve/internal/runtime"
	"steamcmd-serve/internal/secrets"
	"steamcmd-serve/internal/store"
)

// fakeProvider serves zips built in memory. "A-Mod" depends on the BepInEx pack.
type fakeProvider struct{ archives map[string][]byte }

func (f *fakeProvider) Name() string { return "thunderstore" }
func (f *fakeProvider) Search(context.Context, string, string) ([]mods.Package, error) {
	return []mods.Package{{Ref: mods.Ref{ID: "A-Mod", Version: "1.0.0"}, Name: "Mod"}}, nil
}
func (f *fakeProvider) Resolve(_ context.Context, _ string, ref mods.Ref) ([]mods.Ref, error) {
	if ref.ID == "A-Mod" {
		return []mods.Ref{{ID: "denikson-BepInExPack_Valheim", Version: "5.4.2202"}, ref}, nil
	}
	return []mods.Ref{ref}, nil
}
func (f *fakeProvider) Download(_ context.Context, _ string, ref mods.Ref, w io.Writer) (mods.Artifact, error) {
	b, ok := f.archives[ref.ID+"@"+ref.Version]
	if !ok {
		return mods.Artifact{}, mods.ErrManualDownload
	}
	n, err := w.Write(b)
	return mods.Artifact{Filename: ref.ID + ".zip", Size: int64(n)}, err
}

func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for n, body := range files {
		w, _ := zw.Create(n)
		w.Write([]byte(body))
	}
	zw.Close()
	return buf.Bytes()
}

func TestIntegrationMods(t *testing.T) {
	image := os.Getenv("SS_IT_IMAGE")
	if image == "" {
		t.Skip("set SS_IT_IMAGE (make it-image) and DOCKER_HOST to run against a container engine")
	}
	ctx := context.Background()
	base := os.Getenv("SS_IT_DATA_DIR")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".cache", "steamcmd-serve-it")
	}
	dataDir := filepath.Join(base, "mods-"+time.Now().Format("150405.000"))
	os.MkdirAll(dataDir, 0o755)
	t.Cleanup(func() { os.RemoveAll(dataDir) })

	schema, _ := games.FS.ReadFile("manifest.schema.json")
	gs, err := catalog.Load(fstest.MapFS{
		"manifest.schema.json": {Data: schema},
		"moddable/manifest.yaml": {Data: []byte(`
id: moddable
name: Moddable
steam: {serverAppId: 1, platform: linux}
runtime: {image: steamcmd-base, env: {LD_LIBRARY_PATH: "./linux64"}}
launch: {command: ["sh", "-c", "echo READY; env | grep -E '^(DOORSTOP_ENABLED|LD_LIBRARY_PATH)=' | sort; trap 'exit 0' TERM; sleep 600 & wait"], stopTimeoutSeconds: 5}
ports: [{name: game, default: 39500, protocol: udp}]
settings: {schema: s.json}
readiness: {logRegex: "^READY$", timeoutSeconds: 30}
resources: {memoryMB: 128, diskGB: 1}
mods:
  loader: bepinex
  env: {DOORSTOP_ENABLED: "1", LD_LIBRARY_PATH: "./doorstop_libs:./linux64"}
  providers: [{type: thunderstore, community: valheim}]
`)},
		"moddable/s.json": {Data: []byte(`{"type":"object"}`)},
	})
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
	box, _ := secrets.Open(filepath.Join(dataDir, "secret.key"))
	prov := &fakeProvider{archives: map[string][]byte{
		"denikson-BepInExPack_Valheim@5.4.2202": zipOf(t, map[string]string{
			"manifest.json": "{}",
			"BepInExPack_Valheim/BepInEx/core/BepInEx.Preloader.dll": "preloader",
			"BepInExPack_Valheim/libdoorstop_x64.so":                 "doorstop",
		}),
		"A-Mod@1.0.0": zipOf(t, map[string]string{"plugins/A.dll": "a1", "config/a.cfg": "default"}),
		"A-Mod@1.1.0": zipOf(t, map[string]string{"plugins/A.dll": "a2", "config/a.cfg": "default"}),
	}}
	svc, err := New(st, rt, gs, jobs.NewTracker(ctx), Options{
		DataDir: dataDir, HostDataRoot: dataDir, PUID: 1000, PGID: 1000,
		Images: map[string]string{"steamcmd-base": image}, Secrets: box,
		ModProviders: map[string]mods.Provider{"thunderstore": prov},
	})
	if err != nil {
		t.Fatal(err)
	}
	wait := func(j *jobs.Job, err error) *jobs.Job {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
			if cur, _ := svc.Job(j.ID); cur.Status == jobs.Succeeded || cur.Status == jobs.Failed {
				return cur
			}
		}
		t.Fatalf("job %s did not finish", j.ID)
		return nil
	}
	server := func(rel string) string { return filepath.Join(dataDir, "games", "moddable", "%s", rel) }

	inst, _, err := svc.Create(ctx, CreateRequest{Name: "modded", GameID: "moddable"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rt.Remove(context.Background(), containerName(inst.ID)) })
	in := func(rel string) string { return strings.Replace(server(rel), "%s", inst.ID, 1) }
	if _, err := svc.InstallMod(ctx, inst.ID, "thunderstore", mods.Ref{ID: "A-Mod", Version: "1.0.0"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("mods before game install: want ErrConflict, got %v", err)
	}
	wait(svc.Action(ctx, inst.ID, ActionInstall))

	// Install A-Mod: the loader comes along as a dependency.
	if j := wait(svc.InstallMod(ctx, inst.ID, "thunderstore", mods.Ref{ID: "A-Mod", Version: "1.0.0"})); j.Status != jobs.Succeeded {
		t.Fatalf("install mod: %+v", j)
	}
	list, _ := svc.ListMods(ctx, inst.ID)
	if len(list) != 2 || !list[1].Explicit || list[0].Explicit || len(list[0].RequiredBy) != 1 || list[0].RequiredBy[0] != list[1].ID {
		t.Fatalf("mods = %+v", list)
	}
	for _, rel := range []string{"BepInEx/core/BepInEx.Preloader.dll", "libdoorstop_x64.so", "BepInEx/plugins/A-Mod/A.dll", "BepInEx/config/a.cfg"} {
		if _, err := os.Stat(in(rel)); err != nil {
			t.Errorf("missing %s: %v", rel, err)
		}
	}
	if len(list[1].SHA256) != 64 {
		t.Errorf("sha256 not recorded: %q", list[1].SHA256)
	}
	if _, err := svc.UninstallMod(ctx, inst.ID, list[0].ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("uninstall a dependency directly: want ErrConflict, got %v", err)
	}

	// Mod env only while mods are installed; it overrides runtime env.
	wait(svc.Action(ctx, inst.ID, ActionStart))
	var lines []string
	lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	svc.StreamLogs(lctx, inst.ID, 100, func(e LogEvent) error {
		if e.Kind == "log" {
			lines = append(lines, e.Data)
		}
		if len(lines) == 3 {
			cancel()
		}
		return nil
	})
	cancel()
	if got := strings.Join(lines, "|"); got != "READY|DOORSTOP_ENABLED=1|LD_LIBRARY_PATH=./doorstop_libs:./linux64" {
		t.Fatalf("server env = %q", got)
	}
	if _, err := svc.InstallMod(ctx, inst.ID, "thunderstore", mods.Ref{ID: "A-Mod", Version: "1.1.0"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("mod change while running: want ErrConflict, got %v", err)
	}
	wait(svc.Action(ctx, inst.ID, ActionStop))

	// Upgrade keeps identity; user-edited config survives.
	os.WriteFile(in("BepInEx/config/a.cfg"), []byte("edited"), 0o644)
	if j := wait(svc.InstallMod(ctx, inst.ID, "thunderstore", mods.Ref{ID: "A-Mod", Version: "1.1.0"})); j.Status != jobs.Succeeded {
		t.Fatalf("upgrade: %+v", j)
	}
	if b, _ := os.ReadFile(in("BepInEx/plugins/A-Mod/A.dll")); string(b) != "a2" {
		t.Fatalf("upgrade did not replace plugin: %q", b)
	}
	if b, _ := os.ReadFile(in("BepInEx/config/a.cfg")); string(b) != "edited" {
		t.Fatal("upgrade overwrote user config")
	}
	after, _ := svc.ListMods(ctx, inst.ID)
	if len(after) != 2 || after[1].ID != list[1].ID || after[1].Version != "1.1.0" {
		t.Fatalf("after upgrade: %+v", after)
	}

	// A game update re-applies mods that validate may have removed.
	os.Remove(in("BepInEx/plugins/A-Mod/A.dll"))
	if j := wait(svc.Action(ctx, inst.ID, ActionUpdate)); j.Status != jobs.Succeeded {
		t.Fatalf("update: %+v", j)
	}
	if _, err := os.Stat(in("BepInEx/plugins/A-Mod/A.dll")); err != nil {
		t.Fatal("mods not re-applied after update")
	}

	// Manual-download provider error surfaces in the job.
	if j := wait(svc.InstallMod(ctx, inst.ID, "thunderstore", mods.Ref{ID: "B-Mod", Version: "9"})); j.Status != jobs.Failed || !strings.Contains(j.Message, "manual download") {
		t.Fatalf("manual download job: %+v", j)
	}

	// Uninstall the explicit mod: the dependency goes too and the install is clean.
	if j := wait(svc.UninstallMod(ctx, inst.ID, list[1].ID)); j.Status != jobs.Succeeded {
		t.Fatalf("uninstall: %+v", j)
	}
	if rest, _ := svc.ListMods(ctx, inst.ID); len(rest) != 0 {
		t.Fatalf("mods left: %+v", rest)
	}
	for _, rel := range []string{"libdoorstop_x64.so", "BepInEx/core", "BepInEx/plugins/A-Mod"} {
		if _, err := os.Stat(in(rel)); !os.IsNotExist(err) {
			t.Errorf("%s still present after uninstall", rel)
		}
	}
	if entries, _ := os.ReadDir(filepath.Join(dataDir, "instances", inst.ID, "data", "mods", "thunderstore")); len(entries) != 0 {
		t.Errorf("staged archives not pruned: %d left", len(entries))
	}
}
