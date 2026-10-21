// Package instance owns the lifecycle of game-server instances: creation, ports,
// directories, settings and the server container.
package instance

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"steamcmd-serve/internal/catalog"
	"steamcmd-serve/internal/jobs"
	"steamcmd-serve/internal/mods"
	"steamcmd-serve/internal/ports"
	"steamcmd-serve/internal/runtime"
	"steamcmd-serve/internal/secrets"
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
	DataDir       string                   // data root as the controller sees it
	HostDataRoot  string                   // same directory as the container engine's host sees it
	GamesDir      string                   // game install root as the controller sees it (default DataDir/games)
	HostGamesRoot string                   // same directory on the engine's host (default HostDataRoot/games)
	PUID, PGID    int                      // ownership of game files inside game containers
	TZ            string                   // passed to game containers
	Images        map[string]string        // manifest runtime image name -> image reference
	Secrets       *secrets.Box             // encrypts x-secret settings at rest
	ModProviders  map[string]mods.Provider // by manifest provider type ("thunderstore", "nexus")
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
	if opt.Secrets == nil {
		return nil, fmt.Errorf("instance: Options.Secrets is required")
	}
	if opt.GamesDir == "" {
		opt.GamesDir = filepath.Join(opt.DataDir, "games")
	}
	if opt.HostGamesRoot == "" {
		opt.HostGamesRoot = path.Join(opt.HostDataRoot, "games")
	}
	if !filepath.IsAbs(opt.DataDir) || !path.IsAbs(opt.HostDataRoot) || !filepath.IsAbs(opt.GamesDir) || !path.IsAbs(opt.HostGamesRoot) {
		return nil, fmt.Errorf("DATA_DIR, HOST_DATA_ROOT, GAMES_DIR and HOST_GAMES_ROOT must be absolute paths")
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
	rawSettings, err := s.sealSettings(g, settings)
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
		os.RemoveAll(s.serverDir(inst.GameID, inst.ID))
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
		if !resting(inst.State) {
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

// Settings returns an instance's settings with secrets masked as {"hasValue": bool}.
func (s *Service) Settings(ctx context.Context, id string) (catalog.Settings, error) {
	inst, err := s.store.GetInstance(ctx, id)
	if err != nil {
		return nil, err
	}
	g := s.games[inst.GameID]
	if g == nil {
		return nil, fmt.Errorf("game %q no longer available", inst.GameID)
	}
	settings, err := s.openSettings(g, inst)
	if err != nil {
		return nil, err
	}
	return g.MaskSecrets(settings), nil
}

// SetSettings validates and stores settings; they apply on the next start. Secrets sent
// back as {"hasValue": true} keep their stored value. Returns the masked result.
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
	stored, err := s.openSettings(g, inst)
	if err != nil {
		return nil, err
	}
	settings, err := g.CompleteSettings(g.MergeSecrets(in, stored))
	if err != nil {
		return nil, invalid(err)
	}
	if inst.Settings, err = s.sealSettings(g, settings); err != nil {
		return nil, err
	}
	if err := s.store.UpdateInstance(ctx, inst); err != nil {
		return nil, err
	}
	return g.MaskSecrets(settings), nil
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
	if !resting(inst.State) {
		return fmt.Errorf("%w: instance is %s", ErrConflict, inst.State)
	}
	if err := s.rt.Remove(ctx, containerName(id)); err != nil {
		return err
	}
	if err := s.store.DeleteInstance(ctx, id); err != nil {
		return err
	}
	if deleteData {
		if err := os.RemoveAll(s.serverDir(inst.GameID, id)); err != nil {
			return err
		}
		return os.RemoveAll(s.dir(id))
	}
	return nil
}

func (s *Service) serverSpec(g *catalog.Game, inst *store.Instance) (runtime.ServerSpec, error) {
	m := g.Manifest
	settings, err := s.openSettings(g, inst)
	if err != nil {
		return runtime.ServerSpec{}, err
	}
	tctx := s.templateContext(inst, settings)
	args, err := g.LaunchCommand(tctx)
	if err != nil {
		return runtime.ServerSpec{}, err
	}
	workdir := m.Launch.Workdir
	if workdir == "" {
		workdir = "."
	}

	env := s.baseEnv()
	for k, v := range m.Runtime.Env {
		env[k] = v
	}
	if installed, err := s.store.ListMods(context.Background(), inst.ID); err != nil {
		return runtime.ServerSpec{}, err
	} else if len(installed) > 0 {
		for k, v := range m.Mods.Env {
			env[k] = v
		}
	}

	host := s.hostDir(inst.ID)
	binds := []string{
		s.hostServerDir(m.ID, inst.ID) + ":/server",
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
		Restart:     true,
	}, nil
}

// sealSettings encodes settings for storage with secret values encrypted.
func (s *Service) sealSettings(g *catalog.Game, settings catalog.Settings) ([]byte, error) {
	return json.Marshal(g.MapSecrets(settings, func(v any) any {
		if str, ok := v.(string); ok {
			return s.opt.Secrets.Seal(str)
		}
		return v
	}))
}

// openSettings decodes stored settings and decrypts secret values.
func (s *Service) openSettings(g *catalog.Game, inst *store.Instance) (catalog.Settings, error) {
	settings, err := catalog.DecodeSettings(inst.Settings)
	if err != nil {
		return nil, err
	}
	var openErr error
	out := g.MapSecrets(settings, func(v any) any {
		str, ok := v.(string)
		if !ok {
			return v
		}
		pt, err := s.opt.Secrets.Open(str)
		if err != nil && openErr == nil {
			openErr = err
		}
		return pt
	})
	return out, openErr
}

func (s *Service) makeDirs(g *catalog.Game, id string) error {
	dirs := []string{filepath.Join(s.dir(id), "data"), filepath.Join(s.dir(id), "backups"), s.serverDir(g.Manifest.ID, id)}
	for _, p := range g.Manifest.Persist {
		dirs = append(dirs, filepath.Join(s.dir(id), "data", p.Name))
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o775); err != nil {
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

func (s *Service) lock(id string) func() {
	m, _ := s.locks.LoadOrStore(id, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// dir holds an instance's data (saves, rendered config copies) and backups.
func (s *Service) dir(id string) string     { return filepath.Join(s.opt.DataDir, "instances", id) }
func (s *Service) hostDir(id string) string { return path.Join(s.opt.HostDataRoot, "instances", id) }

// serverDir holds an instance's game install: <games root>/<game id>/<instance id>.
func (s *Service) serverDir(gameID, id string) string {
	return filepath.Join(s.opt.GamesDir, gameID, id)
}
func (s *Service) hostServerDir(gameID, id string) string {
	return path.Join(s.opt.HostGamesRoot, gameID, id)
}

func containerName(id string) string { return "steamcmd-serve-" + id }

// resting states have no job or container transition in flight.
func resting(state string) bool {
	switch state {
	case store.StateNew, store.StateStopped, store.StateError:
		return true
	}
	return false
}

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
