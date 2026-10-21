// Package instance owns the lifecycle of game-server instances: creation, ports,
// directories, settings and the server container.
package instance

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"steamcmd-serve/internal/catalog"
	"steamcmd-serve/internal/jobs"
	"steamcmd-serve/internal/ports"
	"steamcmd-serve/internal/runtime"
	"steamcmd-serve/internal/store"
)

// Errors returned by Service; API handlers map them to 404 / 409 / 400.
var (
	ErrNotFound = store.ErrNotFound
	ErrConflict = store.ErrConflict
	ErrInvalid  = errors.New("invalid")
)

// Options configure a Service.
type Options struct {
	DataDir      string            // data root as the controller sees it
	HostDataRoot string            // same directory as the container engine's host sees it
	PUID, PGID   int               // ownership of game files inside game containers
	TZ           string            // passed to game containers
	Images       map[string]string // manifest runtime image name -> image reference
}

// Service manages instances. It is safe for concurrent use.
type Service struct {
	store *store.Store
	rt    *runtime.Docker
	games map[string]*catalog.Game
	jobs  *jobs.Tracker
	opt   Options

	allocMu sync.Mutex // serializes port allocation + insert
	locks   sync.Map   // instance id -> *sync.Mutex, serializes lifecycle ops
}

// New validates opt and returns a Service.
func New(st *store.Store, rt *runtime.Docker, games map[string]*catalog.Game, jt *jobs.Tracker, opt Options) (*Service, error) {
	if !filepath.IsAbs(opt.DataDir) || !path.IsAbs(opt.HostDataRoot) {
		return nil, fmt.Errorf("DATA_DIR and HOST_DATA_ROOT must be absolute paths")
	}
	for _, g := range games {
		if opt.Images[g.Manifest.Runtime.Image] == "" {
			return nil, fmt.Errorf("game %s: no image configured for runtime %q", g.Manifest.ID, g.Manifest.Runtime.Image)
		}
	}
	return &Service{store: st, rt: rt, games: games, jobs: jt, opt: opt}, nil
}

var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 _.-]{0,63}$`)

// CreateRequest is the input to Create (OpenAPI InstanceCreate).
type CreateRequest struct {
	Name     string           `json:"name"`
	GameID   string           `json:"gameId"`
	Ports    map[string]int   `json:"ports"`
	MemoryMB int              `json:"memoryMB"`
	Settings catalog.Settings `json:"settings"`
}

// Create validates req, assigns ports, creates directories and stores the instance.
// It returns preflight warnings (e.g. not enough memory or disk) that do not block creation.
func (s *Service) Create(ctx context.Context, req CreateRequest) (*store.Instance, []string, error) {
	if !namePattern.MatchString(req.Name) {
		return nil, nil, fmt.Errorf("%w: name must match %s", ErrInvalid, namePattern)
	}
	g := s.games[req.GameID]
	if g == nil {
		return nil, nil, fmt.Errorf("%w: unknown game %q", ErrInvalid, req.GameID)
	}
	settings, err := g.CompleteSettings(req.Settings)
	if err != nil {
		return nil, nil, invalid(err)
	}
	rawSettings, err := json.Marshal(settings)
	if err != nil {
		return nil, nil, err
	}
	mem := req.MemoryMB
	if mem == 0 {
		mem = g.Manifest.Resources.MemoryMB
	}
	if mem < 128 {
		return nil, nil, fmt.Errorf("%w: memoryMB must be at least 128", ErrInvalid)
	}

	s.allocMu.Lock()
	defer s.allocMu.Unlock()
	taken, err := s.takenPorts(ctx, "")
	if err != nil {
		return nil, nil, err
	}
	bindings, err := ports.Assign(g.Manifest.Ports, req.Ports, taken)
	if err != nil {
		return nil, nil, portErr(err)
	}

	inst := &store.Instance{
		ID:        strings.ToLower(rand.Text()[:12]),
		Name:      req.Name,
		GameID:    g.Manifest.ID,
		MemoryMB:  mem,
		Settings:  rawSettings,
		State:     store.StateNew,
		CreatedAt: time.Now().UTC(),
		Ports:     bindings,
	}
	if err := s.makeDirs(g, inst.ID); err != nil {
		return nil, nil, err
	}
	if err := s.store.CreateInstance(ctx, inst); err != nil {
		os.RemoveAll(s.dir(inst.ID))
		return nil, nil, err
	}
	return inst, s.preflight(ctx, g, inst), nil
}

// UpdateRequest is the input to Update (OpenAPI InstanceUpdate). Nil fields are unchanged.
type UpdateRequest struct {
	Name     *string        `json:"name"`
	Ports    map[string]int `json:"ports"`
	MemoryMB *int           `json:"memoryMB"`
}

// Update renames an instance, changes its ports (only while not running) or memory limit
// (applied on next start).
func (s *Service) Update(ctx context.Context, id string, req UpdateRequest) (*store.Instance, error) {
	unlock := s.lock(id)
	defer unlock()
	inst, err := s.store.GetInstance(ctx, id)
	if err != nil {
		return nil, err
	}
	if req.Name != nil {
		if !namePattern.MatchString(*req.Name) {
			return nil, fmt.Errorf("%w: name must match %s", ErrInvalid, namePattern)
		}
		inst.Name = *req.Name
	}
	if req.MemoryMB != nil {
		if *req.MemoryMB < 128 {
			return nil, fmt.Errorf("%w: memoryMB must be at least 128", ErrInvalid)
		}
		inst.MemoryMB = *req.MemoryMB
	}

	s.allocMu.Lock()
	defer s.allocMu.Unlock()
	if req.Ports != nil {
		if active(inst.State) {
			return nil, fmt.Errorf("%w: stop the instance before changing ports", ErrConflict)
		}
		g := s.games[inst.GameID]
		if g == nil {
			return nil, fmt.Errorf("game %q no longer available", inst.GameID)
		}
		// Keep current ports for names not in the request.
		requested := map[string]int{}
		for _, p := range g.Manifest.Ports {
			if p.Derive != nil {
				continue
			}
			for _, b := range inst.Ports {
				if b.Name == p.Name {
					requested[p.Name] = b.Port
				}
			}
		}
		for k, v := range req.Ports {
			requested[k] = v
		}
		taken, err := s.takenPorts(ctx, id)
		if err != nil {
			return nil, err
		}
		if inst.Ports, err = ports.Assign(g.Manifest.Ports, requested, taken); err != nil {
			return nil, portErr(err)
		}
	}
	if err := s.store.UpdateInstance(ctx, inst); err != nil {
		return nil, err
	}
	return inst, nil
}

// Settings returns an instance's stored settings.
func (s *Service) Settings(ctx context.Context, id string) (catalog.Settings, error) {
	inst, err := s.store.GetInstance(ctx, id)
	if err != nil {
		return nil, err
	}
	return catalog.DecodeSettings(inst.Settings)
}

// SetSettings validates and stores settings; they apply on the next start.
func (s *Service) SetSettings(ctx context.Context, id string, in catalog.Settings) (catalog.Settings, error) {
	unlock := s.lock(id)
	defer unlock()
	inst, err := s.store.GetInstance(ctx, id)
	if err != nil {
		return nil, err
	}
	g := s.games[inst.GameID]
	if g == nil {
		return nil, fmt.Errorf("game %q no longer available", inst.GameID)
	}
	settings, err := g.CompleteSettings(in)
	if err != nil {
		return nil, invalid(err)
	}
	if inst.Settings, err = json.Marshal(settings); err != nil {
		return nil, err
	}
	if err := s.store.UpdateInstance(ctx, inst); err != nil {
		return nil, err
	}
	return settings, nil
}

// Get returns one instance.
func (s *Service) Get(ctx context.Context, id string) (*store.Instance, error) {
	return s.store.GetInstance(ctx, id)
}

// List returns all instances.
func (s *Service) List(ctx context.Context) ([]*store.Instance, error) {
	return s.store.ListInstances(ctx)
}

// Delete removes a non-running instance; with deleteData it also removes its files.
func (s *Service) Delete(ctx context.Context, id string, deleteData bool) error {
	unlock := s.lock(id)
	defer unlock()
	inst, err := s.store.GetInstance(ctx, id)
	if err != nil {
		return err
	}
	if active(inst.State) {
		return fmt.Errorf("%w: stop the instance first", ErrConflict)
	}
	if err := s.rt.Remove(ctx, containerName(id)); err != nil {
		return err
	}
	if err := s.store.DeleteInstance(ctx, id); err != nil {
		return err
	}
	if deleteData {
		return os.RemoveAll(s.dir(id))
	}
	return nil
}

// Start (re)creates the server container from current settings and starts it.
func (s *Service) Start(ctx context.Context, id string) error {
	unlock := s.lock(id)
	defer unlock()
	inst, err := s.store.GetInstance(ctx, id)
	if err != nil {
		return err
	}
	switch inst.State {
	case store.StateStopped, store.StateError:
	case store.StateNew:
		return fmt.Errorf("%w: install the instance first", ErrConflict)
	default:
		return fmt.Errorf("%w: instance is %s", ErrConflict, inst.State)
	}
	g := s.games[inst.GameID]
	if g == nil {
		return fmt.Errorf("game %q no longer available", inst.GameID)
	}
	spec, err := s.serverSpec(g, inst)
	if err != nil {
		return s.fail(ctx, id, err)
	}
	if err := s.rt.Remove(ctx, spec.Name); err != nil {
		return s.fail(ctx, id, err)
	}
	cid, err := s.rt.Create(ctx, spec)
	if err != nil {
		return s.fail(ctx, id, err)
	}
	if err := s.rt.Start(ctx, cid); err != nil {
		if strings.Contains(err.Error(), "address already in use") || strings.Contains(err.Error(), "port is already allocated") {
			s.rt.Remove(ctx, cid)
			return s.fail(ctx, id, fmt.Errorf("%w: a host port is already in use: %v", ErrConflict, err))
		}
		return s.fail(ctx, id, err)
	}
	return s.store.SetState(ctx, id, store.StateRunning, "")
}

// Stop stops the server container using the manifest's stop signal and timeout.
func (s *Service) Stop(ctx context.Context, id string) error {
	unlock := s.lock(id)
	defer unlock()
	inst, err := s.store.GetInstance(ctx, id)
	if err != nil {
		return err
	}
	if !active(inst.State) {
		return fmt.Errorf("%w: instance is %s", ErrConflict, inst.State)
	}
	if err := s.store.SetState(ctx, id, store.StateStopping, ""); err != nil {
		return err
	}
	if err := s.rt.Stop(ctx, containerName(id)); err != nil {
		return s.fail(ctx, id, err)
	}
	return s.store.SetState(ctx, id, store.StateStopped, "")
}

// Reconcile aligns stored states with the containers actually on the engine. Run it at
// startup: containers outlive the controller (ADR 0001 §2).
func (s *Service) Reconcile(ctx context.Context) error {
	containers, err := s.rt.Managed(ctx)
	if err != nil {
		return err
	}
	running := map[string]bool{}
	known := map[string]bool{}
	list, err := s.store.ListInstances(ctx)
	if err != nil {
		return err
	}
	for _, inst := range list {
		known[inst.ID] = true
	}
	for _, c := range containers {
		if !known[c.InstanceID] {
			slog.Warn("managed container without instance; leaving it alone", "container", c.Name, "instance", c.InstanceID)
			continue
		}
		if c.Role == runtime.RoleServer && c.Running {
			running[c.InstanceID] = true
		}
	}
	for _, inst := range list {
		var want string
		switch {
		case running[inst.ID]:
			want = store.StateRunning
		case active(inst.State):
			want = store.StateStopped
		default:
			continue
		}
		if want != inst.State {
			slog.Info("reconciled instance state", "instance", inst.ID, "from", inst.State, "to", want)
			if err := s.store.SetState(ctx, inst.ID, want, ""); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) serverSpec(g *catalog.Game, inst *store.Instance) (runtime.ServerSpec, error) {
	m := g.Manifest
	settings, err := catalog.DecodeSettings(inst.Settings)
	if err != nil {
		return runtime.ServerSpec{}, err
	}
	tctx := catalog.TemplateContext{Settings: settings, Ports: map[string]int{}, Paths: catalog.Paths{Server: "/server", Data: "/data"}}
	for _, b := range inst.Ports {
		tctx.Ports[b.Name] = b.Port
	}
	args, err := g.LaunchCommand(tctx)
	if err != nil {
		return runtime.ServerSpec{}, err
	}
	workdir := m.Launch.Workdir
	if workdir == "" {
		workdir = "."
	}

	env := map[string]string{}
	for k, v := range m.Runtime.Env {
		env[k] = v
	}
	env["PUID"] = strconv.Itoa(s.opt.PUID)
	env["PGID"] = strconv.Itoa(s.opt.PGID)
	if s.opt.TZ != "" {
		env["TZ"] = s.opt.TZ
	}

	host := s.hostDir(inst.ID)
	binds := []string{
		path.Join(host, "server") + ":/server",
		path.Join(host, "data") + ":/data",
	}
	for _, p := range m.Persist {
		binds = append(binds, path.Join(host, "data", p.Name)+":"+path.Join("/server", p.Path))
	}

	return runtime.ServerSpec{
		Name:  containerName(inst.ID),
		Image: s.opt.Images[m.Runtime.Image],
		Cmd:   append([]string{"run", "--workdir", workdir, "--"}, args...),
		Env:   env,
		Labels: map[string]string{
			runtime.LabelManaged:  "true",
			runtime.LabelInstance: inst.ID,
			runtime.LabelRole:     runtime.RoleServer,
			runtime.LabelGame:     m.ID,
		},
		Binds:       binds,
		Ports:       inst.Ports,
		MemoryBytes: int64(inst.MemoryMB) << 20,
		StopSignal:  m.Launch.StopSignal,
		StopTimeout: m.Launch.StopTimeoutSeconds,
	}, nil
}

func (s *Service) makeDirs(g *catalog.Game, id string) error {
	dirs := []string{"server", "data", "backups"}
	for _, p := range g.Manifest.Persist {
		dirs = append(dirs, filepath.Join("data", p.Name))
	}
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(s.dir(id), d), 0o775); err != nil {
			return err
		}
	}
	return nil
}

// preflight warns when the host looks too small for the instance (ADR 0001; Enshrouded
// needs ~6 GB RAM and 30 GB disk each).
func (s *Service) preflight(ctx context.Context, g *catalog.Game, inst *store.Instance) []string {
	var warnings []string
	if total, err := s.rt.MemTotal(ctx); err == nil && total > 0 {
		need := int64(inst.MemoryMB) << 20
		if list, err := s.store.ListInstances(ctx); err == nil {
			for _, o := range list {
				if o.ID != inst.ID && active(o.State) {
					need += int64(o.MemoryMB) << 20
				}
			}
		}
		if need > total {
			warnings = append(warnings, fmt.Sprintf("running instances plus this one need %d MB but the host has %d MB", need>>20, total>>20))
		}
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(s.opt.DataDir, &st); err == nil {
		free := st.Bavail * uint64(st.Bsize)
		if need := uint64(g.Manifest.Resources.DiskGB) << 30; free < need {
			warnings = append(warnings, fmt.Sprintf("%s needs about %d GB free disk; %d GB available", g.Manifest.Name, need>>30, free>>30))
		}
	}
	return warnings
}

func (s *Service) takenPorts(ctx context.Context, exclude string) (map[ports.Key]bool, error) {
	taken, err := s.store.TakenPorts(ctx, exclude)
	if err != nil {
		return nil, err
	}
	published, err := s.rt.PublishedPorts(ctx, exclude)
	if err != nil {
		return nil, err
	}
	for k := range published {
		taken[k] = true
	}
	return taken, nil
}

func (s *Service) fail(ctx context.Context, id string, err error) error {
	if serr := s.store.SetState(ctx, id, store.StateError, err.Error()); serr != nil {
		slog.Error("record instance error", "instance", id, "err", serr)
	}
	return err
}

func (s *Service) lock(id string) func() {
	m, _ := s.locks.LoadOrStore(id, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func (s *Service) dir(id string) string     { return filepath.Join(s.opt.DataDir, "instances", id) }
func (s *Service) hostDir(id string) string { return path.Join(s.opt.HostDataRoot, "instances", id) }

func containerName(id string) string { return "steamcmd-serve-" + id }

func active(state string) bool {
	switch state {
	case store.StateStarting, store.StateRunning, store.StateStopping:
		return true
	}
	return false
}

func invalid(err error) error { return fmt.Errorf("%w: %v", ErrInvalid, err) }

func portErr(err error) error {
	var ie *ports.InvalidError
	if errors.As(err, &ie) {
		return invalid(err)
	}
	return fmt.Errorf("%w: %v", ErrConflict, err)
}
