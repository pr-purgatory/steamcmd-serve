package instance

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"steamcmd-serve/internal/catalog"
	"steamcmd-serve/internal/jobs"
	"steamcmd-serve/internal/store"
)

// Backup reasons (OpenAPI Backup.reason).
const (
	ReasonManual     = "manual"
	ReasonScheduled  = "scheduled"
	ReasonPreUpdate  = "pre-update"
	ReasonPreRestore = "pre-restore"
)

// keepBackups is how many archives are kept per instance; older ones are deleted.
const keepBackups = 20

// Backup describes one backup archive (OpenAPI Backup).
type Backup struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"createdAt"`
	SizeBytes int64     `json:"sizeBytes"`
	Reason    string    `json:"reason"`
}

var backupID = regexp.MustCompile(`^(\d{8}T\d{9}Z)-(manual|scheduled|pre-update|pre-restore)$`)

const backupTimeLayout = "20060102T150405.000Z"

// Backups lists an instance's backups, newest first.
func (s *Service) Backups(ctx context.Context, id string) ([]Backup, error) {
	if _, err := s.store.GetInstance(ctx, id); err != nil {
		return nil, err
	}
	return s.listBackups(id)
}

// CreateBackup starts a manual backup job. Backups of a running server are taken from
// live files; the game saves periodically (Enshrouded every 5 minutes), so the newest
// minutes may be missing. Stop the server first for a fully consistent copy.
func (s *Service) CreateBackup(ctx context.Context, id string) (*jobs.Job, error) {
	inst, err := s.store.GetInstance(ctx, id)
	if err != nil {
		return nil, err
	}
	g := s.games[inst.GameID]
	if g == nil || !hasBackupPaths(g) {
		return nil, fmt.Errorf("%w: this game declares no backup paths", ErrInvalid)
	}
	switch inst.State {
	case store.StateInstalling, store.StateUpdating:
		return nil, fmt.Errorf("%w: instance is %s", ErrConflict, inst.State)
	}
	return s.jobs.Start(id, "backup", func(ctx context.Context, r jobs.Report) error {
		_, err := s.backup(g, inst, ReasonManual)
		return err
	}), nil
}

// RestoreBackup starts a restore job. The instance must be stopped; current data is
// backed up first so a restore can be undone.
func (s *Service) RestoreBackup(ctx context.Context, id, backupName string) (*jobs.Job, error) {
	if !backupID.MatchString(backupName) {
		return nil, ErrNotFound
	}
	inst, err := s.store.GetInstance(ctx, id)
	if err != nil {
		return nil, err
	}
	g := s.games[inst.GameID]
	if g == nil {
		return nil, fmt.Errorf("game %q no longer available", inst.GameID)
	}
	archive := filepath.Join(s.dir(id), "backups", backupName+".tar.gz")
	if _, err := os.Stat(archive); err != nil {
		return nil, ErrNotFound
	}
	unlock := s.lock(id)
	err = s.store.Transition(ctx, id, []string{store.StateStopped, store.StateError}, store.StateUpdating, "restoring backup")
	unlock()
	if err != nil {
		return nil, err
	}
	return s.jobs.Start(id, "restore", func(ctx context.Context, r jobs.Report) error {
		err := func() error {
			r(0.1, "backing up current data")
			if _, err := s.backup(g, inst, ReasonPreRestore); err != nil {
				return fmt.Errorf("pre-restore backup: %w", err)
			}
			r(0.5, "restoring "+backupName)
			return s.restore(g, id, archive)
		}()
		return s.finish(id, store.StateUpdating, store.StateStopped, err)
	}), nil
}

// ScheduledBackups backs up every running instance whose game declares backup paths.
func (s *Service) ScheduledBackups(ctx context.Context) {
	list, err := s.store.ListInstances(ctx)
	if err != nil {
		return
	}
	for _, inst := range list {
		if g := s.games[inst.GameID]; g != nil && inst.State == store.StateRunning && hasBackupPaths(g) {
			if _, err := s.backup(g, inst, ReasonScheduled); err != nil {
				s.logBackupErr(inst.ID, err)
			}
		}
	}
}

func hasBackupPaths(g *catalog.Game) bool {
	for _, p := range g.Manifest.Persist {
		if p.Backup {
			return true
		}
	}
	return false
}

// backup writes a tar.gz of every persist path marked backup:true and prunes old archives.
func (s *Service) backup(g *catalog.Game, inst *store.Instance, reason string) (*Backup, error) {
	dir := filepath.Join(s.dir(inst.ID), "backups")
	if err := os.MkdirAll(dir, 0o775); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	name := strings.Replace(now.Format(backupTimeLayout), ".", "", 1) + "-" + reason
	final := filepath.Join(dir, name+".tar.gz")
	tmp := final + ".tmp"

	if err := func() error {
		f, err := os.Create(tmp)
		if err != nil {
			return err
		}
		defer f.Close()
		gz := gzip.NewWriter(f)
		tw := tar.NewWriter(gz)
		for _, p := range g.Manifest.Persist {
			if !p.Backup {
				continue
			}
			root := filepath.Join(s.dir(inst.ID), "data", p.Name)
			if err := tw.AddFS(prefixFS{os.DirFS(root), p.Name}); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("archive %s: %w", p.Name, err)
			}
		}
		if err := tw.Close(); err != nil {
			return err
		}
		if err := gz.Close(); err != nil {
			return err
		}
		return f.Sync()
	}(); err != nil {
		os.Remove(tmp)
		return nil, err
	}
	if err := os.Rename(tmp, final); err != nil {
		return nil, err
	}
	s.pruneBackups(inst.ID)
	fi, _ := os.Stat(final)
	return &Backup{ID: name, CreatedAt: now, SizeBytes: fi.Size(), Reason: reason}, nil
}

// restore replaces each backed-up persist dir with the archive's contents.
func (s *Service) restore(g *catalog.Game, id, archive string) error {
	dataDir := filepath.Join(s.dir(id), "data")
	staging, err := os.MkdirTemp(dataDir, ".restore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)

	allowed := map[string]bool{}
	for _, p := range g.Manifest.Persist {
		if p.Backup {
			allowed[p.Name] = true
		}
	}
	if err := extractTarGz(archive, staging, allowed); err != nil {
		return err
	}
	for name := range allowed {
		target := filepath.Join(dataDir, name)
		old := target + ".old"
		os.RemoveAll(old)
		if err := os.Rename(target, old); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		src := filepath.Join(staging, name)
		if _, err := os.Stat(src); errors.Is(err, fs.ErrNotExist) {
			if err := os.MkdirAll(target, 0o775); err != nil {
				return err
			}
		} else if err := os.Rename(src, target); err != nil {
			os.Rename(old, target)
			return err
		}
		os.RemoveAll(old)
	}
	return nil
}

// extractTarGz extracts regular files and dirs whose first path element is in allowed.
// Absolute paths, "..", links and other entry types are rejected.
func extractTarGz(archive, dest string, allowed map[string]bool) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.FromSlash(h.Name)
		if !filepath.IsLocal(name) || !allowed[strings.SplitN(filepath.ToSlash(name), "/", 2)[0]] {
			return fmt.Errorf("backup contains unexpected path %q", h.Name)
		}
		target := filepath.Join(dest, name)
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o775); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o775); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o664)
			if err != nil {
				return err
			}
			_, err = io.Copy(out, tr)
			if cerr := out.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("backup entry %q has unsupported type %c", h.Name, h.Typeflag)
		}
	}
}

func (s *Service) listBackups(id string) ([]Backup, error) {
	entries, err := os.ReadDir(filepath.Join(s.dir(id), "backups"))
	if errors.Is(err, fs.ErrNotExist) {
		return []Backup{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []Backup{}
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".tar.gz")
		m := backupID.FindStringSubmatch(name)
		if !ok || m == nil {
			continue
		}
		ts := m[1][:15] + "." + m[1][15:]
		created, err := time.Parse(backupTimeLayout, ts)
		if err != nil {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, Backup{ID: name, CreatedAt: created, SizeBytes: info.Size(), Reason: m[2]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (s *Service) pruneBackups(id string) {
	list, err := s.listBackups(id)
	if err != nil {
		return
	}
	for _, b := range list[min(len(list), keepBackups):] {
		os.Remove(filepath.Join(s.dir(id), "backups", b.ID+".tar.gz"))
	}
}

func (s *Service) logBackupErr(id string, err error) {
	slog.Warn("scheduled backup failed", "instance", id, "err", err)
}

// prefixFS presents fsys under a top-level directory name, so tar entries are
// "<persist name>/<path>".
type prefixFS struct {
	fs.FS
	prefix string
}

func (p prefixFS) Open(name string) (fs.File, error) {
	if name == "." {
		return &prefixRoot{name: p.prefix}, nil
	}
	if name == p.prefix {
		return p.FS.Open(".")
	}
	rest, ok := strings.CutPrefix(name, p.prefix+"/")
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return p.FS.Open(rest)
}

func (p prefixFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if name == "." {
		fi, err := fs.Stat(p.FS, ".")
		if err != nil {
			return nil, err
		}
		return []fs.DirEntry{fs.FileInfoToDirEntry(renamed{fi, p.prefix})}, nil
	}
	if name == p.prefix {
		return fs.ReadDir(p.FS, ".")
	}
	rest, ok := strings.CutPrefix(name, p.prefix+"/")
	if !ok {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrNotExist}
	}
	return fs.ReadDir(p.FS, rest)
}

type prefixRoot struct{ name string }

func (r *prefixRoot) Stat() (fs.FileInfo, error) { return rootInfo{}, nil }
func (r *prefixRoot) Read([]byte) (int, error)   { return 0, io.EOF }
func (r *prefixRoot) Close() error               { return nil }

type rootInfo struct{}

func (rootInfo) Name() string       { return "." }
func (rootInfo) Size() int64        { return 0 }
func (rootInfo) Mode() fs.FileMode  { return fs.ModeDir | 0o755 }
func (rootInfo) ModTime() time.Time { return time.Time{} }
func (rootInfo) IsDir() bool        { return true }
func (rootInfo) Sys() any           { return nil }

type renamed struct {
	fs.FileInfo
	name string
}

func (r renamed) Name() string { return r.name }
