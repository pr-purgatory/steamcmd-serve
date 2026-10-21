package instance

import (
	"bytes"
	"context"
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
	"time"

	"steamcmd-serve/internal/catalog"
	"steamcmd-serve/internal/jobs"
	"steamcmd-serve/internal/runtime"
	"steamcmd-serve/internal/store"
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

// Games returns the game catalog.
func (s *Service) Games() map[string]*catalog.Game { return s.games }

// Job returns a job snapshot (jobs.ErrNotFound if unknown).
func (s *Service) Job(id string) (*jobs.Job, error) { return s.jobs.Get(id) }

// Action runs a lifecycle action in the background and returns its job.
// Unknown actions return ErrInvalid; actions not allowed in the current state ErrConflict.
//
// The instance state is the lock: each action atomically moves the instance from an
// allowed resting state into a transitional one (installing, updating, starting,
// stopping) and only the job that did so moves it out again.
func (s *Service) Action(ctx context.Context, id, action string) (*jobs.Job, error) {
	inst, err := s.store.GetInstance(ctx, id)
	if err != nil {
		return nil, err
	}
	g := s.games[inst.GameID]
	if g == nil {
		return nil, fmt.Errorf("game %q no longer available", inst.GameID)
	}

	type plan struct {
		from []string
		to   string
		run  func(context.Context, jobs.Report) error
	}
	var p plan
	switch action {
	case ActionInstall:
		p = plan{[]string{store.StateNew, store.StateError}, store.StateInstalling, func(ctx context.Context, r jobs.Report) error {
			return s.finish(id, store.StateInstalling, store.StateStopped, s.install(ctx, g, inst, r))
		}}
	case ActionUpdate:
		p = plan{[]string{store.StateStopped, store.StateError}, store.StateUpdating, func(ctx context.Context, r jobs.Report) error {
			err := func() error {
				if hasBackupPaths(g) {
					r(0, "backing up before update")
					if _, err := s.backup(g, inst, ReasonPreUpdate); err != nil {
						return fmt.Errorf("pre-update backup: %w", err)
					}
				}
				if err := s.install(ctx, g, inst, r); err != nil {
					return err
				}
				r(0.95, "re-applying mods")
				return s.reapplyMods(ctx, g, inst.ID)
			}()
			return s.finish(id, store.StateUpdating, store.StateStopped, err)
		}}
	case ActionStart:
		p = plan{[]string{store.StateStopped, store.StateError}, store.StateStarting, func(ctx context.Context, r jobs.Report) error {
			return s.start(ctx, g, id, r)
		}}
	case ActionStop:
		p = plan{[]string{store.StateStarting, store.StateRunning}, store.StateStopping, func(ctx context.Context, r jobs.Report) error {
			return s.stop(ctx, g, id)
		}}
	case ActionRestart:
		p = plan{[]string{store.StateRunning}, store.StateStopping, func(ctx context.Context, r jobs.Report) error {
			if err := s.stop(ctx, g, id); err != nil {
				return err
			}
			if err := s.store.Transition(context.Background(), id, []string{store.StateStopped}, store.StateStarting, ""); err != nil {
				return err
			}
			return s.start(ctx, g, id, r)
		}}
	default:
		return nil, fmt.Errorf("%w: unknown action %q", ErrInvalid, action)
	}

	unlock := s.lock(id)
	err = s.store.Transition(ctx, id, p.from, p.to, "")
	unlock()
	if err != nil {
		return nil, err
	}
	return s.jobs.Start(id, action, p.run), nil
}

// finish moves an instance out of the transitional state `from`.
func (s *Service) finish(id, from, to string, err error) error {
	if err != nil {
		to = store.StateError
	}
	detail := ""
	if err != nil {
		detail = err.Error()
	}
	if terr := s.store.Transition(context.Background(), id, []string{from}, to, detail); terr != nil {
		slog.Error("finish transition", "instance", id, "from", from, "to", to, "err", terr)
	}
	return err
}

// ---- install / update ------------------------------------------------------

var steamProgress = regexp.MustCompile(`progress: ([0-9.]+) \((\d+) / (\d+)\)`)

// install runs steamcmd app_update in a one-shot job container (ADR 0001 §3).
func (s *Service) install(ctx context.Context, g *catalog.Game, inst *store.Instance, report jobs.Report) error {
	m := g.Manifest
	if err := os.MkdirAll(filepath.Join(s.opt.DataDir, "steamcmd"), 0o775); err != nil {
		return err
	}
	cmd := []string{"install", strconv.Itoa(m.Steam.ServerAppID), "--platform", m.Steam.Platform, "--validate"}
	if m.Steam.Branch != "" {
		cmd = append(cmd, "--branch", m.Steam.Branch)
	}
	spec := runtime.ServerSpec{
		Name:  containerName(inst.ID) + "-job",
		Image: s.opt.Images["steamcmd-base"],
		Cmd:   cmd,
		Env:   s.baseEnv(),
		Labels: map[string]string{
			runtime.LabelManaged:  "true",
			runtime.LabelInstance: inst.ID,
			runtime.LabelRole:     runtime.RoleJob,
			runtime.LabelGame:     m.ID,
		},
		Binds: []string{
			s.hostServerDir(m.ID, inst.ID) + ":/server",
			path.Join(s.opt.HostDataRoot, "steamcmd") + ":/steamcmd",
		},
		StopTimeout: 30,
	}
	report(0, "starting steamcmd")
	var last lastLines
	success := false
	code, err := s.runJob(ctx, spec, func(line string) {
		last.add(line)
		if strings.Contains(line, "Success! App") {
			success = true
		}
		if mm := steamProgress.FindStringSubmatch(line); mm != nil {
			if pct, err := strconv.ParseFloat(mm[1], 64); err == nil {
				report(pct/100, strings.TrimSpace(line))
			}
		}
	})
	switch {
	case err != nil:
		return err
	case code != 0:
		return fmt.Errorf("steamcmd exited with %d: %s", code, last.String())
	case !success:
		return fmt.Errorf("steamcmd finished without a success message: %s", last.String())
	}
	return nil
}

// runJob runs a one-shot container to completion, streaming its output to onLine.
func (s *Service) runJob(ctx context.Context, spec runtime.ServerSpec, onLine func(string)) (int64, error) {
	if err := s.rt.Remove(ctx, spec.Name); err != nil {
		return -1, err
	}
	cid, err := s.rt.Create(ctx, spec)
	if err != nil {
		return -1, err
	}
	defer s.rt.Remove(context.Background(), cid)
	if err := s.rt.Start(ctx, cid); err != nil {
		return -1, err
	}
	logsDone := make(chan struct{})
	go func() {
		defer close(logsDone)
		s.rt.FollowLogs(ctx, cid, runtime.LogOptions{Tail: -1, Follow: true}, func(l string) error { onLine(l); return nil })
	}()
	code, err := s.rt.Wait(ctx, cid)
	<-logsDone
	return code, err
}

// ---- start / stop ----------------------------------------------------------

var errReady = errors.New("ready")

// start renders config, (re)creates and starts the server container, then waits for the
// manifest's readiness log line. The instance is in `starting` on entry.
func (s *Service) start(ctx context.Context, g *catalog.Game, id string, report jobs.Report) error {
	inst, err := s.store.GetInstance(ctx, id)
	if err != nil {
		return err
	}
	spec, err := s.serverSpec(g, inst)
	if err == nil {
		report(0.1, "rendering config")
		err = s.renderConfig(g, inst)
	}
	var cid string
	if err == nil {
		if err = s.rt.Remove(ctx, spec.Name); err == nil {
			cid, err = s.rt.Create(ctx, spec)
		}
	}
	started := time.Now()
	if err == nil {
		report(0.2, "starting container")
		if err = s.rt.Start(ctx, cid); err != nil {
			if msg := err.Error(); strings.Contains(msg, "address already in use") || strings.Contains(msg, "port is already allocated") {
				err = fmt.Errorf("a host port is already in use by another program: %v", err)
			}
		}
	}
	if err != nil {
		return s.finish(id, store.StateStarting, store.StateError, err)
	}

	report(0.5, "waiting for server to be ready")
	timeout := time.Duration(g.Manifest.Readiness.TimeoutSeconds) * time.Second
	if timeout == 0 {
		timeout = 10 * time.Minute
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var last lastLines
	ferr := s.rt.FollowLogs(rctx, cid, runtime.LogOptions{Tail: -1, Follow: true, Since: started.Add(-time.Second)}, func(line string) error {
		last.add(line)
		if g.Ready.MatchString(line) {
			return errReady
		}
		return nil
	})

	detail := ""
	switch {
	case errors.Is(ferr, errReady):
	case errors.Is(ferr, context.DeadlineExceeded):
		detail = fmt.Sprintf("readiness line not seen within %s; server may still be loading", timeout)
	case ctx.Err() != nil:
		return ctx.Err() // controller shutting down; boot reconcile repairs state
	default:
		// Log stream ended: the server exited (or a stop request removed it).
		if running, _ := s.rt.Running(context.Background(), cid); !running {
			cur, _ := s.store.GetInstance(context.Background(), id)
			if cur != nil && cur.State != store.StateStarting {
				return nil // stopped by the user while starting
			}
			return s.finish(id, store.StateStarting, store.StateError, fmt.Errorf("server exited during start: %s", last.String()))
		}
	}
	if err := s.store.Transition(context.Background(), id, []string{store.StateStarting}, store.StateRunning, detail); err != nil && !errors.Is(err, store.ErrConflict) {
		return err
	}
	return nil
}

// stop stops the server container. The instance is in `stopping` on entry.
func (s *Service) stop(ctx context.Context, g *catalog.Game, id string) error {
	if err := s.rt.Stop(ctx, containerName(id)); err != nil {
		return s.finish(id, store.StateStopping, store.StateError, err)
	}
	detail := ""
	if drift, _ := s.configDrift(g, id); drift {
		detail = fmt.Sprintf("the game changed %s while running; it is rendered from settings again on next start", g.Manifest.Settings.File.Path)
	}
	return s.store.Transition(context.Background(), id, []string{store.StateStopping}, store.StateStopped, detail)
}

// ---- config rendering --------------------------------------------------------

const renderedDir = ".steamcmd-serve"

// renderConfig writes the manifest's config file from settings before every start
// (ADR 0001 §5) and keeps a copy to detect changes made by the game.
func (s *Service) renderConfig(g *catalog.Game, inst *store.Instance) error {
	f := g.Manifest.Settings.File
	if f == nil {
		return nil
	}
	settings, err := s.openSettings(g, inst)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	if g.ConfigTemplate != nil {
		if err := g.ConfigTemplate.Execute(&buf, s.templateContext(inst, settings)); err != nil {
			return fmt.Errorf("render %s: %w", f.Path, err)
		}
	}
	target, err := s.serverPath(inst.GameID, inst.ID, f.Path)
	if err != nil {
		return err
	}
	if err := writeAtomic(target, buf.Bytes()); err != nil {
		return err
	}
	return writeAtomic(s.renderedCopy(inst.ID, f.Path), buf.Bytes())
}

// configDrift reports whether the game rewrote its config file since the last render.
func (s *Service) configDrift(g *catalog.Game, id string) (bool, error) {
	f := g.Manifest.Settings.File
	if f == nil {
		return false, nil
	}
	target, err := s.serverPath(g.Manifest.ID, id, f.Path)
	if err != nil {
		return false, err
	}
	want, err := os.ReadFile(s.renderedCopy(id, f.Path))
	if err != nil {
		return false, nil // never rendered
	}
	got, err := os.ReadFile(target)
	if err != nil {
		return false, nil
	}
	return !bytes.Equal(want, got), nil
}

func (s *Service) templateContext(inst *store.Instance, settings catalog.Settings) catalog.TemplateContext {
	ctx := catalog.TemplateContext{Settings: settings, Ports: map[string]int{}, Paths: catalog.Paths{Server: "/server", Data: "/data"}}
	for _, b := range inst.Ports {
		ctx.Ports[b.Name] = b.Port
	}
	return ctx
}

// serverPath resolves a manifest-relative path inside the instance's install dir.
func (s *Service) serverPath(gameID, id, rel string) (string, error) {
	base := s.serverDir(gameID, id)
	p := filepath.Join(base, filepath.FromSlash(rel))
	if !strings.HasPrefix(p, base+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the install dir", rel)
	}
	return p, nil
}

func (s *Service) renderedCopy(id, rel string) string {
	return filepath.Join(s.dir(id), "data", renderedDir, strings.ReplaceAll(rel, "/", "_")+".rendered")
}

func writeAtomic(target string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o775); err != nil {
		return err
	}
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, data, 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, target)
}

// ---- reconcile ---------------------------------------------------------------

// Reconcile aligns stored states with the containers on the engine. With boot=true it
// also repairs transitional states left by jobs that died with the previous controller;
// otherwise instances with a job in flight are left alone.
func (s *Service) Reconcile(ctx context.Context, boot bool) error {
	containers, err := s.rt.Managed(ctx)
	if err != nil {
		return err
	}
	list, err := s.store.ListInstances(ctx)
	if err != nil {
		return err
	}
	known := map[string]bool{}
	for _, inst := range list {
		known[inst.ID] = true
	}
	running := map[string]bool{}
	for _, c := range containers {
		switch {
		case !known[c.InstanceID]:
			if boot {
				slog.Warn("managed container without instance; leaving it alone", "container", c.Name, "instance", c.InstanceID)
			}
		case c.Role == runtime.RoleJob && boot:
			s.rt.Remove(ctx, c.ID) // orphaned install job
		case c.Role == runtime.RoleServer && c.Running:
			running[c.InstanceID] = true
		}
	}

	for _, inst := range list {
		var to, detail string
		switch inst.State {
		case store.StateInstalling, store.StateUpdating:
			if boot {
				to, detail = store.StateError, "interrupted by controller restart; run install again"
			}
		case store.StateStarting, store.StateStopping:
			if boot {
				to = store.StateStopped
				if running[inst.ID] {
					to = store.StateRunning
				}
			}
		case store.StateRunning:
			if !running[inst.ID] {
				to, detail = store.StateError, "server container is not running (crashed or stopped outside steamcmd-serve)"
			}
		case store.StateStopped, store.StateError:
			if running[inst.ID] {
				to = store.StateRunning
			}
		}
		if to == "" || to == inst.State {
			continue
		}
		if err := s.store.Transition(ctx, inst.ID, []string{inst.State}, to, detail); err == nil {
			slog.Info("reconciled instance state", "instance", inst.ID, "from", inst.State, "to", to)
		}
	}
	return nil
}

// ---- logs --------------------------------------------------------------------

// StreamLogs sends the last tail lines of the server log, then follows it (re-attaching
// when the server is restarted), and emits state changes, until ctx is done or emit
// returns an error.
func (s *Service) StreamLogs(ctx context.Context, id string, tail int, emit func(LogEvent) error) error {
	inst, err := s.store.GetInstance(ctx, id)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var mu sync.Mutex
	var emitErr error
	send := func(e LogEvent) error {
		mu.Lock()
		defer mu.Unlock()
		if emitErr == nil {
			if emitErr = emit(e); emitErr != nil {
				cancel()
			}
		}
		return emitErr
	}
	if err := send(LogEvent{Kind: "state", Data: inst.State}); err != nil {
		return err
	}

	go func() {
		state := inst.State
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				cur, err := s.store.GetInstance(ctx, id)
				if err != nil {
					continue
				}
				if cur.State != state {
					state = cur.State
					send(LogEvent{Kind: "state", Data: state})
				}
			}
		}
	}()

	opts := runtime.LogOptions{Tail: tail, Follow: true}
	for ctx.Err() == nil {
		attached := time.Now()
		s.rt.FollowLogs(ctx, containerName(id), opts, func(line string) error {
			return send(LogEvent{Kind: "log", Data: line})
		})
		// Container gone or stopped: wait, then re-attach to new output only.
		opts = runtime.LogOptions{Tail: -1, Follow: true, Since: attached}
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
	}
	mu.Lock()
	defer mu.Unlock()
	return emitErr
}

// ---- helpers -----------------------------------------------------------------

func (s *Service) baseEnv() map[string]string {
	env := map[string]string{"PUID": strconv.Itoa(s.opt.PUID), "PGID": strconv.Itoa(s.opt.PGID)}
	if s.opt.TZ != "" {
		env["TZ"] = s.opt.TZ
	}
	return env
}

// lastLines keeps the most recent output lines for error messages.
type lastLines struct {
	mu    sync.Mutex
	lines []string
}

func (l *lastLines) add(line string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if strings.TrimSpace(line) == "" {
		return
	}
	l.lines = append(l.lines, line)
	if len(l.lines) > 5 {
		l.lines = l.lines[1:]
	}
}

func (l *lastLines) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, " | ")
}
