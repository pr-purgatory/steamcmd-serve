package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/legacy"

	"steamcmd-serve/internal/catalog"
	"steamcmd-serve/internal/instance"
	"steamcmd-serve/internal/jobs"
	"steamcmd-serve/internal/mods"
	"steamcmd-serve/internal/ports"
	"steamcmd-serve/internal/store"
)

var t0 = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// fake implements Instances without Docker. When err is set every call returns it.
type fake struct {
	err    error
	games  map[string]*catalog.Game
	events []instance.LogEvent
	// received values, for assertions
	create      instance.CreateRequest
	settings    catalog.Settings
	gotTail     int
	deleted     *bool
	modProvider string
	modRef      mods.Ref
	upload      string
}

func (f *fake) inst() *store.Instance {
	return &store.Instance{ID: "abc123", Name: "My Server", GameID: "enshrouded", MemoryMB: 6144,
		State: store.StateStopped, CreatedAt: t0,
		Ports: []ports.Binding{{Name: "query", Port: 15637, Protocol: "udp"}}}
}
func (f *fake) job() *jobs.Job {
	return &jobs.Job{ID: "job1", InstanceID: "abc123", Kind: "install", Status: jobs.Running, CreatedAt: t0}
}

func (f *fake) List(context.Context) ([]*store.Instance, error) {
	return []*store.Instance{f.inst()}, f.err
}
func (f *fake) Get(context.Context, string) (*store.Instance, error) {
	return f.inst(), f.err
}
func (f *fake) Create(_ context.Context, req instance.CreateRequest) (*store.Instance, []string, error) {
	f.create = req
	if f.err != nil {
		return nil, nil, f.err
	}
	return f.inst(), []string{"low disk"}, nil
}
func (f *fake) Update(context.Context, string, instance.UpdateRequest) (*store.Instance, error) {
	return f.inst(), f.err
}
func (f *fake) Delete(_ context.Context, _ string, d bool) error { f.deleted = &d; return f.err }
func (f *fake) Settings(context.Context, string) (catalog.Settings, error) {
	return catalog.Settings{"name": "x", "password": map[string]any{"hasValue": true}}, f.err
}
func (f *fake) SetSettings(_ context.Context, _ string, in catalog.Settings) (catalog.Settings, error) {
	f.settings = in
	return in, f.err
}
func (f *fake) Action(context.Context, string, string) (*jobs.Job, error) { return f.job(), f.err }
func (f *fake) Job(string) (*jobs.Job, error)                             { return f.job(), f.err }
func (f *fake) StreamLogs(ctx context.Context, _ string, tail int, emit func(instance.LogEvent) error) error {
	f.gotTail = tail
	if f.err != nil {
		return f.err
	}
	for _, e := range f.events {
		if err := emit(e); err != nil {
			return err
		}
	}
	<-ctx.Done()
	return ctx.Err()
}
func (f *fake) Backups(context.Context, string) ([]instance.Backup, error) {
	return []instance.Backup{{ID: "b1", CreatedAt: t0, SizeBytes: 10, Reason: "manual"}}, f.err
}
func (f *fake) CreateBackup(context.Context, string) (*jobs.Job, error) { return f.job(), f.err }
func (f *fake) RestoreBackup(context.Context, string, string) (*jobs.Job, error) {
	return f.job(), f.err
}
func (f *fake) Games() map[string]*catalog.Game { return f.games }
func (f *fake) SearchMods(context.Context, string, string, string) ([]mods.Package, error) {
	return []mods.Package{{Ref: mods.Ref{ID: "A-Mod", Version: "1.0.0"}, Name: "Mod"}}, f.err
}
func (f *fake) ListMods(context.Context, string) ([]*store.Mod, error) {
	return []*store.Mod{{ID: "m1", Provider: "thunderstore", PackageID: "A-Mod", Version: "1.0.0",
		SHA256: strings.Repeat("a", 64), Explicit: true, InstalledAt: t0}}, f.err
}
func (f *fake) InstallMod(_ context.Context, _, provider string, ref mods.Ref) (*jobs.Job, error) {
	f.modProvider, f.modRef = provider, ref
	return f.job(), f.err
}
func (f *fake) InstallModUpload(_ context.Context, _, provider string, ref mods.Ref, r io.Reader) (*jobs.Job, error) {
	b, _ := io.ReadAll(r)
	f.modProvider, f.modRef, f.upload = provider, ref, string(b)
	return f.job(), f.err
}
func (f *fake) UninstallMod(context.Context, string, string) (*jobs.Job, error) {
	return f.job(), f.err
}

func testGames(t *testing.T) map[string]*catalog.Game {
	t.Helper()
	g := &catalog.Game{SettingsRaw: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}}}`)}
	m := &g.Manifest
	m.ID, m.Name = "enshrouded", "Enshrouded"
	m.Steam.Platform, m.Steam.ServerAppID, m.Steam.GameAppID = "windows", 2278520, 1203620
	m.Resources.MemoryMB, m.Resources.DiskGB = 6144, 30
	m.Ports = []catalog.Port{
		{Name: "game", Default: 15636, Protocol: "udp", Description: "Game port"},
		{Name: "query", Default: 15637, Protocol: "udp"},
	}
	m.Ports[1].Derive = &struct {
		From   string `yaml:"from"`
		Offset int    `yaml:"offset"`
	}{From: "game", Offset: 1}
	m.Mods.Providers = []struct {
		Type       string `yaml:"type"`
		Community  string `yaml:"community"`
		GameDomain string `yaml:"gameDomain"`
	}{{Type: "thunderstore"}}
	g2 := &catalog.Game{SettingsRaw: json.RawMessage(`{"type":"object"}`)}
	g2.Manifest.ID, g2.Manifest.Name, g2.Manifest.Steam.Platform = "aaa-game", "AAA", "linux"
	return map[string]*catalog.Game{"enshrouded": g, "aaa-game": g2}
}

type env struct {
	t      *testing.T
	f      *fake
	mux    *http.ServeMux
	router routers.Router
}

func newEnv(t *testing.T) *env { return newEnvNexus(t, nil) }

func newEnvNexus(t *testing.T, n NexusSettings) *env {
	t.Helper()
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatalf("load spec: %v", err)
	}
	doc.Servers = openapi3.Servers{{URL: "http://localhost/api/v1"}}
	if err := doc.Validate(loader.Context); err != nil {
		t.Fatalf("validate spec: %v", err)
	}
	router, err := legacy.NewRouter(doc)
	if err != nil {
		t.Fatal(err)
	}
	f := &fake{games: testGames(t)}
	mux := http.NewServeMux()
	New(f).WithNexus(n).Register(mux)
	return &env{t: t, f: f, mux: mux, router: router}
}

// do sends a request through the mux and validates the response against the spec.
func (e *env) do(method, path, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	rr := e.serve(method, path, body)
	if strings.HasPrefix(rr.Header().Get("Content-Type"), "text/event-stream") {
		return rr
	}
	req := httptest.NewRequest(method, "http://localhost/api/v1"+path, nil)
	route, pathParams, err := e.router.FindRoute(req)
	if err != nil {
		e.t.Fatalf("%s %s: route not in spec: %v", method, path, err)
	}
	in := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: &openapi3filter.RequestValidationInput{Request: req, PathParams: pathParams, Route: route},
		Status:                 rr.Code,
		Header:                 rr.Header(),
		Body:                   io.NopCloser(bytes.NewReader(rr.Body.Bytes())),
		Options:                &openapi3filter.Options{IncludeResponseStatus: true},
	}
	if err := openapi3filter.ValidateResponse(context.Background(), in); err != nil {
		e.t.Errorf("%s %s -> %d does not match spec: %v\nbody: %s", method, path, rr.Code, err, rr.Body)
	}
	return rr
}

func (e *env) serve(method, path, body string) *httptest.ResponseRecorder {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, "/api/v1"+path, rd)
	if strings.HasSuffix(path, "/logs") {
		// streams until the client goes away
		ctx, cancel := context.WithTimeout(req.Context(), 100*time.Millisecond)
		defer cancel()
		req = req.WithContext(ctx)
	}
	rr := httptest.NewRecorder()
	e.mux.ServeHTTP(rr, req)
	return rr
}

func decodeErr(t *testing.T, rr *httptest.ResponseRecorder) errorBody {
	t.Helper()
	var eb errorBody
	if err := json.Unmarshal(rr.Body.Bytes(), &eb); err != nil {
		t.Fatalf("not an Error body: %q", rr.Body)
	}
	return eb
}

func TestRoutesRegistered(t *testing.T) {
	e := newEnv(t)
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for path, item := range doc.Paths.Map() {
		if path == "/healthz" || path == "/setup" || strings.HasPrefix(path, "/auth/") {
			continue
		}
		url := path
		for _, name := range []string{"gameId", "instanceId", "jobId", "backupId", "modId"} {
			url = strings.ReplaceAll(url, "{"+name+"}", map[string]string{"gameId": "enshrouded", "instanceId": "abc123", "jobId": "job1", "backupId": "b1", "modId": "m1"}[name])
		}
		url = strings.ReplaceAll(url, "{action}", "start")
		for method := range item.Operations() {
			n++
			rr := e.serve(method, url, "{}")
			isMuxMiss := rr.Code == http.StatusMethodNotAllowed ||
				(rr.Code == http.StatusNotFound && !strings.HasPrefix(rr.Header().Get("Content-Type"), "application/json"))
			if isMuxMiss {
				t.Errorf("%s %s: not routed (status %d)", method, path, rr.Code)
			}
		}
	}
	if n < 20 {
		t.Errorf("only %d operations checked", n)
	}
}

func TestGames(t *testing.T) {
	e := newEnv(t)
	rr := e.do("GET", "/games", "")
	if rr.Code != 200 {
		t.Fatalf("status %d", rr.Code)
	}
	var list []gameBody
	json.Unmarshal(rr.Body.Bytes(), &list)
	if len(list) != 2 || list[0].ID != "aaa-game" || list[1].ID != "enshrouded" {
		t.Fatalf("not sorted by id: %+v", list)
	}
	if list[0].ModLoader != "none" {
		t.Errorf("modLoader default: %q", list[0].ModLoader)
	}

	rr = e.do("GET", "/games/enshrouded", "")
	if rr.Code != 200 {
		t.Fatalf("status %d", rr.Code)
	}
	var d struct {
		gameBody
		SettingsSchema map[string]any `json:"settingsSchema"`
	}
	json.Unmarshal(rr.Body.Bytes(), &d)
	if d.SettingsSchema["type"] != "object" || len(d.Ports) != 2 || d.Ports[1].DerivedFrom != "game" ||
		d.Ports[1].Offset == nil || *d.Ports[1].Offset != 1 || d.Ports[0].Description != "Game port" ||
		d.ModProviders[0] != "thunderstore" || d.Resources.DiskGB != 30 {
		t.Errorf("unexpected game: %s", rr.Body)
	}

	rr = e.do("GET", "/games/nope", "")
	if rr.Code != 404 || decodeErr(t, rr).Code != "not_found" {
		t.Errorf("unknown game: %d %s", rr.Code, rr.Body)
	}
}

func TestInstances(t *testing.T) {
	e := newEnv(t)
	if rr := e.do("GET", "/instances", ""); rr.Code != 200 {
		t.Errorf("list %d", rr.Code)
	}
	if rr := e.do("GET", "/instances/abc123", ""); rr.Code != 200 {
		t.Errorf("get %d", rr.Code)
	}

	rr := e.do("POST", "/instances", `{"name":"My Server","gameId":"enshrouded","settings":{"port":15637,"big":12345678901234567890}}`)
	if rr.Code != 201 {
		t.Fatalf("create %d %s", rr.Code, rr.Body)
	}
	var created struct {
		ID       string   `json:"id"`
		Warnings []string `json:"warnings"`
	}
	json.Unmarshal(rr.Body.Bytes(), &created)
	if created.ID != "abc123" || len(created.Warnings) != 1 {
		t.Errorf("create body: %s", rr.Body)
	}
	if got := fmt.Sprint(e.f.create.Settings["big"]); got != "12345678901234567890" {
		t.Errorf("settings number not preserved: %s", got)
	}
	if _, ok := e.f.create.Settings["port"].(json.Number); !ok {
		t.Errorf("settings numbers should be json.Number, got %T", e.f.create.Settings["port"])
	}

	if rr := e.do("PATCH", "/instances/abc123", `{"name":"New"}`); rr.Code != 200 {
		t.Errorf("patch %d", rr.Code)
	}
	if rr := e.do("DELETE", "/instances/abc123?deleteData=true", ""); rr.Code != 204 || e.f.deleted == nil || !*e.f.deleted {
		t.Errorf("delete %d", rr.Code)
	}
	if rr := e.do("DELETE", "/instances/abc123", ""); rr.Code != 204 || *e.f.deleted {
		t.Errorf("delete default %d", rr.Code)
	}
}

func TestInstanceBadRequests(t *testing.T) {
	e := newEnv(t)
	cases := []struct{ method, path, body string }{
		{"POST", "/instances", `{"name":`},
		{"POST", "/instances", `{"name":"x","gameId":"g","bogus":1}`},
		{"POST", "/instances", `{"name":5,"gameId":"g"}`},
		{"POST", "/instances", ``},
		{"POST", "/instances", `{"name":"x","gameId":"g"} {}`},
		{"PATCH", "/instances/abc123", `{"unknown":1}`},
		{"PATCH", "/instances/abc123", `{}`},
		{"PATCH", "/instances/abc123", `{"memoryMB":"lots"}`},
		{"PUT", "/instances/abc123/settings", `[1,2]`},
		{"PUT", "/instances/abc123/settings", `null`},
		{"PUT", "/instances/abc123/settings", `{"a":`},
	}
	for _, c := range cases {
		rr := e.do(c.method, c.path, c.body)
		if rr.Code != 400 || decodeErr(t, rr).Code != "bad_request" {
			t.Errorf("%s %s %q: %d %s", c.method, c.path, c.body, rr.Code, rr.Body)
		}
	}
	// Decoder errors can quote input; make sure secrets in bodies never come back.
	rr := e.serve("PUT", "/instances/abc123/settings", `{"password":"hunter2-secret",`)
	if strings.Contains(rr.Body.String(), "hunter2") {
		t.Errorf("body echoed: %s", rr.Body)
	}
	if rr := e.serve("DELETE", "/instances/abc123?deleteData=maybe", ""); rr.Code != 400 {
		t.Errorf("deleteData=maybe: %d", rr.Code)
	}
}

func TestBodyTooLarge(t *testing.T) {
	e := newEnv(t)
	big := `{"name":"` + strings.Repeat("a", 2<<20) + `"}`
	rr := e.serve("POST", "/instances", big)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status %d", rr.Code)
	}
}

func TestErrorMapping(t *testing.T) {
	groups := []struct {
		name         string
		method, path string
		body         string
		// statuses the spec declares for the operation
		declared map[int]bool
	}{
		{"get instance", "GET", "/instances/abc123", "", map[int]bool{404: true}},
		{"create instance", "POST", "/instances", `{"name":"x","gameId":"enshrouded"}`, map[int]bool{400: true, 409: true}},
		{"update instance", "PATCH", "/instances/abc123", `{"name":"y"}`, map[int]bool{400: true, 404: true, 409: true}},
		{"delete instance", "DELETE", "/instances/abc123", "", map[int]bool{404: true, 409: true}},
		{"action", "POST", "/instances/abc123/actions/start", "", map[int]bool{404: true, 409: true}},
		{"settings get", "GET", "/instances/abc123/settings", "", map[int]bool{404: true}},
		{"settings put", "PUT", "/instances/abc123/settings", `{"a":1}`, map[int]bool{400: true, 404: true}},
		{"backups list", "GET", "/instances/abc123/backups", "", map[int]bool{404: true}},
		{"backup create", "POST", "/instances/abc123/backups", "", map[int]bool{404: true, 409: true}},
		{"backup restore", "POST", "/instances/abc123/backups/b1/restore", "", map[int]bool{404: true, 409: true}},
		{"job", "GET", "/jobs/job1", "", map[int]bool{404: true}},
	}
	errs := []struct {
		err    error
		status int
		code   string
	}{
		{fmt.Errorf("%w: instance x", instance.ErrNotFound), 404, "not_found"},
		{fmt.Errorf("wrapped: %w", jobs.ErrNotFound), 404, "not_found"},
		{fmt.Errorf("%w: stop it first", instance.ErrConflict), 409, "conflict"},
		{fmt.Errorf("%w: bad name", instance.ErrInvalid), 400, "invalid"},
	}
	for _, g := range groups {
		for _, c := range errs {
			e := newEnv(t)
			e.f.err = c.err
			var rr *httptest.ResponseRecorder
			if g.declared[c.status] {
				rr = e.do(g.method, g.path, g.body) // also validates against the spec
			} else {
				rr = e.serve(g.method, g.path, g.body)
			}
			if rr.Code != c.status {
				t.Errorf("%s with %v: status %d, want %d", g.name, c.err, rr.Code, c.status)
				continue
			}
			eb := decodeErr(t, rr)
			if eb.Code != c.code || eb.Message != c.err.Error() {
				t.Errorf("%s: body %+v", g.name, eb)
			}
		}
	}
}

func TestInternalErrorsNotEchoed(t *testing.T) {
	secret := "dial tcp 10.0.0.5: password=hunter2 token=abcdef"
	e := newEnv(t)
	e.f.err = errors.New(secret)
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/instances", ""},
		{"GET", "/instances/abc123", ""},
		{"POST", "/instances", `{"name":"x","gameId":"enshrouded"}`},
		{"PUT", "/instances/abc123/settings", `{"password":"hunter2"}`},
		{"POST", "/instances/abc123/actions/stop", ""},
		{"GET", "/jobs/job1", ""},
		{"GET", "/instances/abc123/logs", ""},
	} {
		rr := e.serve(c.method, c.path, c.body)
		if rr.Code != 500 {
			t.Errorf("%s %s: %d", c.method, c.path, rr.Code)
			continue
		}
		if strings.Contains(rr.Body.String(), "hunter2") || strings.Contains(rr.Body.String(), "dial") {
			t.Errorf("%s %s echoed internal error: %s", c.method, c.path, rr.Body)
		}
		if eb := decodeErr(t, rr); eb.Code != "internal" || eb.Message != "internal error" {
			t.Errorf("%s %s: %+v", c.method, c.path, eb)
		}
	}
}

func TestActions(t *testing.T) {
	e := newEnv(t)
	for _, a := range []string{"install", "update", "start", "stop", "restart"} {
		rr := e.do("POST", "/instances/abc123/actions/"+a, "")
		if rr.Code != 202 {
			t.Errorf("%s: %d", a, rr.Code)
		}
	}
	e.f.err = errors.New("must not be called")
	rr := e.serve("POST", "/instances/abc123/actions/explode", "")
	if rr.Code != 400 || decodeErr(t, rr).Code != "bad_request" {
		t.Errorf("unknown action: %d %s", rr.Code, rr.Body)
	}
}

func TestSettings(t *testing.T) {
	e := newEnv(t)
	rr := e.do("GET", "/instances/abc123/settings", "")
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"hasValue":true`) {
		t.Errorf("get settings: %d %s", rr.Code, rr.Body)
	}
	rr = e.do("PUT", "/instances/abc123/settings", `{"name":"x","slots":16,"password":{"hasValue":true}}`)
	if rr.Code != 200 {
		t.Fatalf("put: %d %s", rr.Code, rr.Body)
	}
	if _, ok := e.f.settings["slots"].(json.Number); !ok {
		t.Errorf("want json.Number, got %T", e.f.settings["slots"])
	}
}

func TestBackupsAndJobs(t *testing.T) {
	e := newEnv(t)
	if rr := e.do("GET", "/instances/abc123/backups", ""); rr.Code != 200 {
		t.Errorf("list %d", rr.Code)
	}
	if rr := e.do("POST", "/instances/abc123/backups", ""); rr.Code != 202 {
		t.Errorf("create %d", rr.Code)
	}
	if rr := e.do("POST", "/instances/abc123/backups/b1/restore", ""); rr.Code != 202 {
		t.Errorf("restore %d", rr.Code)
	}
	rr := e.do("GET", "/jobs/job1", "")
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"status":"running"`) {
		t.Errorf("job %d %s", rr.Code, rr.Body)
	}
}

func TestMods(t *testing.T) {
	e := newEnv(t)
	if rr := e.do("GET", "/games/enshrouded/mods?provider=thunderstore&q=x", ""); rr.Code != 200 || !strings.Contains(rr.Body.String(), `"dependencies":[]`) {
		t.Errorf("search %d %s", rr.Code, rr.Body)
	}
	if rr := e.do("GET", "/games/enshrouded/mods?provider=curseforge", ""); rr.Code != 400 {
		t.Errorf("bad provider %d", rr.Code)
	}
	if rr := e.do("GET", "/instances/abc123/mods", ""); rr.Code != 200 || !strings.Contains(rr.Body.String(), `"requiredBy":[]`) {
		t.Errorf("list %d %s", rr.Code, rr.Body)
	}
	rr := e.do("POST", "/instances/abc123/mods", `{"provider":"thunderstore","ref":{"id":"A-Mod","version":"1.0.0"}}`)
	if rr.Code != 202 || e.f.modRef.ID != "A-Mod" || e.f.modProvider != "thunderstore" {
		t.Errorf("install %d %s %+v", rr.Code, rr.Body, e.f.modRef)
	}
	if rr := e.do("POST", "/instances/abc123/mods", `{"provider":"thunderstore"}`); rr.Code != 400 {
		t.Errorf("install without ref %d", rr.Code)
	}
	if rr := e.do("DELETE", "/instances/abc123/mods/m1", ""); rr.Code != 202 {
		t.Errorf("uninstall %d", rr.Code)
	}

	// Multipart upload: fields then file.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("provider", "nexus")
	mw.WriteField("ref", `{"id":"123:456","version":"2.0"}`)
	fw, _ := mw.CreateFormFile("file", "mod.zip")
	fw.Write([]byte("PK-zip-bytes"))
	mw.Close()
	req := httptest.NewRequest("POST", "/api/v1/instances/abc123/mods", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	if rec.Code != 202 || e.f.upload != "PK-zip-bytes" || e.f.modRef.ID != "123:456" || e.f.modProvider != "nexus" {
		t.Errorf("upload %d %s %+v %q", rec.Code, rec.Body, e.f.modRef, e.f.upload)
	}

	// File before ref is rejected.
	buf.Reset()
	mw = multipart.NewWriter(&buf)
	fw, _ = mw.CreateFormFile("file", "mod.zip")
	fw.Write([]byte("x"))
	mw.Close()
	req = httptest.NewRequest("POST", "/api/v1/instances/abc123/mods", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec = httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Errorf("file first: %d", rec.Code)
	}
}

func TestLogsBadTail(t *testing.T) {
	e := newEnv(t)
	for _, tail := range []string{"-1", "5001", "abc"} {
		rr := e.serve("GET", "/instances/abc123/logs?tail="+tail, "")
		if rr.Code != 400 {
			t.Errorf("tail=%s: %d", tail, rr.Code)
		}
	}
	e.f.err = fmt.Errorf("%w: nope", instance.ErrNotFound)
	if rr := e.do("GET", "/instances/abc123/logs", ""); rr.Code != 404 {
		t.Errorf("not found: %d", rr.Code)
	}
}

// syncRecorder lets the test read the body while the handler is still running.
type syncRecorder struct {
	*httptest.ResponseRecorder
	mu chan struct{}
}

func newSyncRecorder() *syncRecorder {
	r := &syncRecorder{ResponseRecorder: httptest.NewRecorder(), mu: make(chan struct{}, 1)}
	r.mu <- struct{}{}
	return r
}
func (r *syncRecorder) Write(b []byte) (int, error) {
	<-r.mu
	defer func() { r.mu <- struct{}{} }()
	return r.ResponseRecorder.Write(b)
}
func (r *syncRecorder) WriteString(s string) (int, error) { return r.Write([]byte(s)) }
func (r *syncRecorder) body() string {
	<-r.mu
	defer func() { r.mu <- struct{}{} }()
	return r.ResponseRecorder.Body.String()
}

func TestLogsSSE(t *testing.T) {
	e := newEnv(t)
	e.f.events = []instance.LogEvent{
		{Kind: "log", Data: "hello world"},
		{Kind: "log", Data: "line one\nline two"},
		{Kind: "state", Data: "running"},
	}
	h := New(e.f)
	h.keepalive = 20 * time.Millisecond
	mux := http.NewServeMux()
	h.Register(mux)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest("GET", "/api/v1/instances/abc123/logs?tail=7", nil).WithContext(ctx)
	rr := newSyncRecorder()
	done := make(chan struct{})
	go func() { mux.ServeHTTP(rr, req); close(done) }()

	want := "event: log\ndata: hello world\n\n" +
		"event: log\ndata: line one\ndata: line two\n\n" +
		"event: state\ndata: running\n\n"
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(rr.body(), ": keepalive\n\n") {
		if time.Now().After(deadline) {
			t.Fatalf("no keepalive; body %q", rr.body())
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case <-done:
		t.Fatal("handler returned before client disconnected")
	default:
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("handler did not return after context cancel")
	}

	got := strings.ReplaceAll(rr.ResponseRecorder.Body.String(), ": keepalive\n\n", "")
	if got != want {
		t.Errorf("wire format:\n got %q\nwant %q", got, want)
	}
	if !strings.HasPrefix(rr.ResponseRecorder.Body.String(), want[:20]) {
		t.Errorf("events should precede keepalive")
	}
	hdr := rr.Header()
	if hdr.Get("Content-Type") != "text/event-stream" || hdr.Get("Cache-Control") != "no-cache" || hdr.Get("X-Accel-Buffering") != "no" {
		t.Errorf("headers: %v", hdr)
	}
	if !rr.Flushed {
		t.Error("not flushed")
	}
	if e.f.gotTail != 7 {
		t.Errorf("tail %d", e.f.gotTail)
	}
}

func TestLogsDefaultTail(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("GET", "/api/v1/instances/abc123/logs", nil).WithContext(ctx)
	done := make(chan struct{})
	go func() { e.mux.ServeHTTP(httptest.NewRecorder(), req); close(done) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done
	if e.f.gotTail != 500 {
		t.Errorf("default tail %d", e.f.gotTail)
	}
}
