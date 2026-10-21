// Package api implements the REST handlers described by api/openapi.yaml.
//
// Request bodies and settings may contain game passwords: they are never logged.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"steamcmd-serve/internal/catalog"
	"steamcmd-serve/internal/instance"
	"steamcmd-serve/internal/jobs"
	"steamcmd-serve/internal/mods"
	"steamcmd-serve/internal/ports"
	"steamcmd-serve/internal/store"
)

const (
	maxBody           = 1 << 20
	defaultTail       = 500
	maxTail           = 5000
	keepaliveInterval = 15 * time.Second
)

// Instances is the part of *instance.Service the handlers use.
type Instances interface {
	List(ctx context.Context) ([]*store.Instance, error)
	Get(ctx context.Context, id string) (*store.Instance, error)
	Create(ctx context.Context, req instance.CreateRequest) (*store.Instance, []string, error)
	Update(ctx context.Context, id string, req instance.UpdateRequest) (*store.Instance, error)
	Delete(ctx context.Context, id string, deleteData bool) error
	Settings(ctx context.Context, id string) (catalog.Settings, error)
	SetSettings(ctx context.Context, id string, in catalog.Settings) (catalog.Settings, error)
	Action(ctx context.Context, id, action string) (*jobs.Job, error)
	Job(id string) (*jobs.Job, error)
	StreamLogs(ctx context.Context, id string, tail int, emit func(instance.LogEvent) error) error
	Backups(ctx context.Context, id string) ([]instance.Backup, error)
	CreateBackup(ctx context.Context, id string) (*jobs.Job, error)
	RestoreBackup(ctx context.Context, id, backupID string) (*jobs.Job, error)
	Games() map[string]*catalog.Game
	SearchMods(ctx context.Context, gameID, provider, query string) ([]mods.Package, error)
	ListMods(ctx context.Context, id string) ([]*store.Mod, error)
	InstallMod(ctx context.Context, id, provider string, ref mods.Ref) (*jobs.Job, error)
	InstallModUpload(ctx context.Context, id, provider string, ref mods.Ref, archive io.Reader) (*jobs.Job, error)
	UninstallMod(ctx context.Context, id, modID string) (*jobs.Job, error)
}

var _ Instances = (*instance.Service)(nil)

// Handler serves the REST API.
type Handler struct {
	svc Instances
	// keepalive is the SSE comment interval; tests shorten it.
	keepalive time.Duration
}

// New returns a Handler backed by svc.
func New(svc Instances) *Handler {
	return &Handler{svc: svc, keepalive: keepaliveInterval}
}

// Register adds all routes under /api/v1 to mux.
func (h *Handler) Register(mux *http.ServeMux) {
	const p = "/api/v1"
	routes := []struct {
		pattern string
		fn      http.HandlerFunc
	}{
		{"GET /games", h.listGames},
		{"GET /games/{gameId}", h.getGame},
		{"GET /instances", h.listInstances},
		{"POST /instances", h.createInstance},
		{"GET /instances/{instanceId}", h.getInstance},
		{"PATCH /instances/{instanceId}", h.updateInstance},
		{"DELETE /instances/{instanceId}", h.deleteInstance},
		{"POST /instances/{instanceId}/actions/{action}", h.instanceAction},
		{"GET /instances/{instanceId}/settings", h.getSettings},
		{"PUT /instances/{instanceId}/settings", h.putSettings},
		{"GET /instances/{instanceId}/logs", h.streamLogs},
		{"GET /instances/{instanceId}/backups", h.listBackups},
		{"POST /instances/{instanceId}/backups", h.createBackup},
		{"POST /instances/{instanceId}/backups/{backupId}/restore", h.restoreBackup},
		{"GET /jobs/{jobId}", h.getJob},
		{"GET /games/{gameId}/mods", h.searchMods},
		{"GET /instances/{instanceId}/mods", h.listInstanceMods},
		{"POST /instances/{instanceId}/mods", h.installMod},
		{"DELETE /instances/{instanceId}/mods/{modId}", h.uninstallMod},
		// The NexusMods task implements these.
		{"PUT /settings/nexus-api-key", notImplemented},
		{"GET /settings/nexus-api-key", notImplemented},
		{"DELETE /settings/nexus-api-key", notImplemented},
	}
	for _, r := range routes {
		method, path, _ := strings.Cut(r.pattern, " ")
		mux.Handle(method+" "+p+path, r.fn)
	}
}

// ---- responses and errors ----

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		slog.Error("api: encode response", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(append(b, '\n'))
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, errorBody{Code: code, Message: msg})
}

func badRequest(w http.ResponseWriter, msg string) {
	writeError(w, http.StatusBadRequest, "bad_request", msg)
}

func notImplemented(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotImplemented, "not_implemented", "not implemented")
}

// writeErr maps service errors to the OpenAPI Error response.
func writeErr(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, instance.ErrNotFound), errors.Is(err, jobs.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, instance.ErrConflict):
		writeError(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, instance.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid", err.Error())
	default:
		slog.Error("api: internal error", "method", r.Method, "path", r.URL.Path, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "internal error")
	}
}

// decodeBody reads a JSON request body (max 1 MiB) into v. Numbers in untyped values are
// json.Number. Errors are reported as generic messages: decoder errors can quote input.
func decodeBody(w http.ResponseWriter, r *http.Request, v any, strict bool) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.UseNumber()
	if strict {
		dec.DisallowUnknownFields()
	}
	if err := dec.Decode(v); err != nil {
		var mbe *http.MaxBytesError
		switch {
		case errors.As(err, &mbe):
			writeError(w, http.StatusRequestEntityTooLarge, "bad_request", "request body too large")
		case errors.Is(err, io.EOF):
			badRequest(w, "request body is required")
		default:
			badRequest(w, "malformed JSON or wrong field types")
		}
		return false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		badRequest(w, "unexpected data after JSON body")
		return false
	}
	return true
}

// ---- games ----

type portSpec struct {
	Name        string `json:"name"`
	Default     int    `json:"default"`
	Protocol    string `json:"protocol"`
	DerivedFrom string `json:"derivedFrom,omitempty"`
	Offset      *int   `json:"offset,omitempty"`
	Description string `json:"description,omitempty"`
}

type gameResources struct {
	MemoryMB int `json:"memoryMB"`
	DiskGB   int `json:"diskGB"`
}

type gameBody struct {
	ID           string        `json:"id"`
	Name         string        `json:"name"`
	Platform     string        `json:"platform"`
	ServerAppID  int           `json:"serverAppId,omitempty"`
	GameAppID    int           `json:"gameAppId,omitempty"`
	Ports        []portSpec    `json:"ports"`
	ModLoader    string        `json:"modLoader"`
	ModProviders []string      `json:"modProviders"`
	Resources    gameResources `json:"resources"`
}

type gameDetail struct {
	gameBody
	SettingsSchema json.RawMessage `json:"settingsSchema"`
}

func toGame(g *catalog.Game) gameBody {
	m := &g.Manifest
	b := gameBody{
		ID:           m.ID,
		Name:         m.Name,
		Platform:     m.Steam.Platform,
		ServerAppID:  m.Steam.ServerAppID,
		GameAppID:    m.Steam.GameAppID,
		Ports:        make([]portSpec, 0, len(m.Ports)),
		ModLoader:    m.Mods.Loader,
		ModProviders: make([]string, 0, len(m.Mods.Providers)),
		Resources:    gameResources{MemoryMB: m.Resources.MemoryMB, DiskGB: m.Resources.DiskGB},
	}
	if b.ModLoader == "" {
		b.ModLoader = "none"
	}
	for _, p := range m.Ports {
		ps := portSpec{Name: p.Name, Default: p.Default, Protocol: p.Protocol, Description: p.Description}
		if p.Derive != nil {
			off := p.Derive.Offset
			ps.DerivedFrom, ps.Offset = p.Derive.From, &off
		}
		b.Ports = append(b.Ports, ps)
	}
	for _, p := range m.Mods.Providers {
		b.ModProviders = append(b.ModProviders, p.Type)
	}
	return b
}

func (h *Handler) listGames(w http.ResponseWriter, r *http.Request) {
	games := h.svc.Games()
	out := make([]gameBody, 0, len(games))
	for _, id := range catalog.IDs(games) {
		out = append(out, toGame(games[id]))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) getGame(w http.ResponseWriter, r *http.Request) {
	g := h.svc.Games()[r.PathValue("gameId")]
	if g == nil {
		writeError(w, http.StatusNotFound, "not_found", "game not found")
		return
	}
	schema := g.SettingsRaw
	if len(schema) == 0 {
		schema = json.RawMessage(`{}`)
	}
	writeJSON(w, http.StatusOK, gameDetail{gameBody: toGame(g), SettingsSchema: schema})
}

// ---- instances ----

// instanceBody makes sure ports serialises as [] rather than null.
type instanceBody struct {
	*store.Instance
	Warnings []string `json:"warnings,omitempty"`
}

func toInstance(i *store.Instance) *store.Instance {
	if i.Ports == nil {
		c := *i
		c.Ports = []ports.Binding{}
		return &c
	}
	return i
}

func (h *Handler) listInstances(w http.ResponseWriter, r *http.Request) {
	list, err := h.svc.List(r.Context())
	if err != nil {
		writeErr(w, r, err)
		return
	}
	sort.SliceStable(list, func(a, b int) bool { return list[a].CreatedAt.Before(list[b].CreatedAt) })
	out := make([]*store.Instance, 0, len(list))
	for _, i := range list {
		out = append(out, toInstance(i))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) createInstance(w http.ResponseWriter, r *http.Request) {
	var req instance.CreateRequest
	if !decodeBody(w, r, &req, true) {
		return
	}
	inst, warnings, err := h.svc.Create(r.Context(), req)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, instanceBody{Instance: toInstance(inst), Warnings: warnings})
}

func (h *Handler) getInstance(w http.ResponseWriter, r *http.Request) {
	inst, err := h.svc.Get(r.Context(), r.PathValue("instanceId"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toInstance(inst))
}

func (h *Handler) updateInstance(w http.ResponseWriter, r *http.Request) {
	var req instance.UpdateRequest
	if !decodeBody(w, r, &req, true) {
		return
	}
	if req.Name == nil && req.Ports == nil && req.MemoryMB == nil {
		badRequest(w, "at least one of name, ports, memoryMB is required")
		return
	}
	inst, err := h.svc.Update(r.Context(), r.PathValue("instanceId"), req)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toInstance(inst))
}

func (h *Handler) deleteInstance(w http.ResponseWriter, r *http.Request) {
	deleteData := false
	if v := r.URL.Query().Get("deleteData"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			badRequest(w, "deleteData must be a boolean")
			return
		}
		deleteData = b
	}
	if err := h.svc.Delete(r.Context(), r.PathValue("instanceId"), deleteData); err != nil {
		writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) instanceAction(w http.ResponseWriter, r *http.Request) {
	action := r.PathValue("action")
	switch action {
	case instance.ActionInstall, instance.ActionUpdate, instance.ActionStart, instance.ActionStop, instance.ActionRestart:
	default:
		badRequest(w, fmt.Sprintf("unknown action %q", action))
		return
	}
	h.job(w, r)(h.svc.Action(r.Context(), r.PathValue("instanceId"), action))
}

// job returns a function that writes a 202 Job response (or the error).
func (h *Handler) job(w http.ResponseWriter, r *http.Request) func(*jobs.Job, error) {
	return func(j *jobs.Job, err error) {
		if err != nil {
			writeErr(w, r, err)
			return
		}
		writeJSON(w, http.StatusAccepted, j)
	}
}

// ---- settings ----

func (h *Handler) getSettings(w http.ResponseWriter, r *http.Request) {
	s, err := h.svc.Settings(r.Context(), r.PathValue("instanceId"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeSettings(w, s)
}

func (h *Handler) putSettings(w http.ResponseWriter, r *http.Request) {
	var in catalog.Settings
	if !decodeBody(w, r, &in, false) {
		return
	}
	if in == nil {
		badRequest(w, "settings must be a JSON object")
		return
	}
	s, err := h.svc.SetSettings(r.Context(), r.PathValue("instanceId"), in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeSettings(w, s)
}

func writeSettings(w http.ResponseWriter, s catalog.Settings) {
	if s == nil {
		s = catalog.Settings{}
	}
	writeJSON(w, http.StatusOK, s)
}

// ---- backups and jobs ----

func (h *Handler) listBackups(w http.ResponseWriter, r *http.Request) {
	list, err := h.svc.Backups(r.Context(), r.PathValue("instanceId"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if list == nil {
		list = []instance.Backup{}
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *Handler) createBackup(w http.ResponseWriter, r *http.Request) {
	h.job(w, r)(h.svc.CreateBackup(r.Context(), r.PathValue("instanceId")))
}

func (h *Handler) restoreBackup(w http.ResponseWriter, r *http.Request) {
	h.job(w, r)(h.svc.RestoreBackup(r.Context(), r.PathValue("instanceId"), r.PathValue("backupId")))
}

func (h *Handler) getJob(w http.ResponseWriter, r *http.Request) {
	j, err := h.svc.Job(r.PathValue("jobId"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, j)
}

// ---- logs (SSE) ----

func (h *Handler) streamLogs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("instanceId")
	tail := defaultTail
	if v := r.URL.Query().Get("tail"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > maxTail {
			badRequest(w, fmt.Sprintf("tail must be an integer between 0 and %d", maxTail))
			return
		}
		tail = n
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal", "streaming unsupported")
		return
	}
	// Fail with a normal 404 before the stream starts.
	if _, err := h.svc.Get(r.Context(), id); err != nil {
		writeErr(w, r, err)
		return
	}

	hdr := w.Header()
	hdr.Set("Content-Type", "text/event-stream")
	hdr.Set("Cache-Control", "no-cache")
	hdr.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fl.Flush()

	var mu sync.Mutex // emit and keepalive write from different goroutines
	write := func(s string) error {
		mu.Lock()
		defer mu.Unlock()
		if _, err := io.WriteString(w, s); err != nil {
			return err
		}
		fl.Flush()
		return nil
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(h.keepalive)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-r.Context().Done():
				return
			case <-t.C:
				if write(": keepalive\n\n") != nil {
					return
				}
			}
		}
	}()

	err := h.svc.StreamLogs(r.Context(), id, tail, func(ev instance.LogEvent) error {
		kind := ev.Kind
		if kind != "state" {
			kind = "log"
		}
		return write(formatEvent(kind, ev.Data))
	})
	close(stop)
	wg.Wait()
	if err != nil && r.Context().Err() == nil {
		// Headers are already sent; just record why the stream ended.
		slog.Warn("api: log stream ended", "instance", id, "err", err)
	}
}

func formatEvent(kind, data string) string {
	var b strings.Builder
	b.WriteString("event: " + kind + "\n")
	data = strings.ReplaceAll(data, "\r\n", "\n")
	for _, line := range strings.Split(data, "\n") {
		b.WriteString("data: " + line + "\n")
	}
	b.WriteString("\n")
	return b.String()
}
