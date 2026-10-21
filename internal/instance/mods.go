package instance

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"steamcmd-serve/internal/catalog"
	"steamcmd-serve/internal/jobs"
	"steamcmd-serve/internal/mods"
	"steamcmd-serve/internal/store"
)

// maxUpload bounds manually uploaded mod archives.
const maxUpload = 1 << 30

// SearchMods searches a provider declared by the game's manifest.
func (s *Service) SearchMods(ctx context.Context, gameID, provider, query string) ([]mods.Package, error) {
	g := s.games[gameID]
	if g == nil {
		return nil, ErrNotFound
	}
	p, scope, err := s.provider(g, provider)
	if err != nil {
		return nil, err
	}
	return p.Search(ctx, scope, query)
}

// ListMods returns an instance's installed mods.
func (s *Service) ListMods(ctx context.Context, id string) ([]*store.Mod, error) {
	if _, err := s.store.GetInstance(ctx, id); err != nil {
		return nil, err
	}
	return s.store.ListMods(ctx, id)
}

// InstallMod installs ref and its dependencies from a provider into a stopped instance.
// A provider that needs a manual download fails the job with mods.ErrManualDownload in
// its message; the client then uploads the file with InstallModUpload.
func (s *Service) InstallMod(ctx context.Context, id, provider string, ref mods.Ref) (*jobs.Job, error) {
	inst, g, err := s.modTarget(ctx, id)
	if err != nil {
		return nil, err
	}
	p, scope, err := s.provider(g, provider)
	if err != nil {
		return nil, err
	}
	return s.modJob(ctx, inst, "mod-install", func(ctx context.Context, r jobs.Report) error {
		r(0.05, "resolving dependencies")
		refs, err := p.Resolve(ctx, scope, ref)
		if err != nil {
			return err
		}
		return s.installRefs(ctx, g, inst, provider, refs, r, func(ctx context.Context, ref mods.Ref, w io.Writer) (mods.Artifact, error) {
			return p.Download(ctx, scope, ref, w)
		})
	})
}

// InstallModUpload installs a manually downloaded archive (no dependency resolution).
func (s *Service) InstallModUpload(ctx context.Context, id, provider string, ref mods.Ref, archive io.Reader) (*jobs.Job, error) {
	inst, g, err := s.modTarget(ctx, id)
	if err != nil {
		return nil, err
	}
	if _, _, err := s.provider(g, provider); err != nil {
		return nil, err
	}
	if !validPackageID(ref.ID) || ref.Version == "" {
		return nil, fmt.Errorf("%w: invalid mod reference", ErrInvalid)
	}
	// Spool the request body before the request ends.
	tmp, err := s.spool(inst.ID, io.LimitReader(archive, maxUpload+1))
	if err != nil {
		return nil, err
	}
	if fi, _ := os.Stat(tmp); fi != nil && fi.Size() > maxUpload {
		os.Remove(tmp)
		return nil, fmt.Errorf("%w: archive larger than %d bytes", ErrInvalid, maxUpload)
	}
	return s.modJob(ctx, inst, "mod-install", func(ctx context.Context, r jobs.Report) error {
		defer os.Remove(tmp)
		return s.installRefs(ctx, g, inst, provider, []mods.Ref{ref}, r, func(_ context.Context, _ mods.Ref, w io.Writer) (mods.Artifact, error) {
			f, err := os.Open(tmp)
			if err != nil {
				return mods.Artifact{}, err
			}
			defer f.Close()
			n, err := io.Copy(w, f)
			return mods.Artifact{Filename: ref.ID + ".zip", Size: n}, err
		})
	})
}

// UninstallMod removes an explicitly installed mod and dependencies nothing else needs.
func (s *Service) UninstallMod(ctx context.Context, id, modID string) (*jobs.Job, error) {
	inst, g, err := s.modTarget(ctx, id)
	if err != nil {
		return nil, err
	}
	current, err := s.store.ListMods(ctx, id)
	if err != nil {
		return nil, err
	}
	idx := slices.IndexFunc(current, func(m *store.Mod) bool { return m.ID == modID })
	if idx < 0 {
		return nil, ErrNotFound
	}
	if target := current[idx]; !target.Explicit {
		return nil, fmt.Errorf("%w: %s is a dependency of %s", ErrConflict, target.PackageID, strings.Join(target.RequiredBy, ", "))
	}
	return s.modJob(ctx, inst, "mod-uninstall", func(ctx context.Context, r jobs.Report) error {
		current, err := s.store.ListMods(ctx, id)
		if err != nil {
			return err
		}
		var next []*store.Mod
		for _, m := range current {
			if m.ID == modID {
				continue
			}
			m.RequiredBy = slices.DeleteFunc(m.RequiredBy, func(x string) bool { return x == modID })
			if !m.Explicit && len(m.RequiredBy) == 0 {
				continue
			}
			next = append(next, m)
		}
		r(0.5, "removing files")
		return s.commitMods(ctx, g, inst, next)
	})
}

// installRefs downloads, verifies and stages refs (dependencies first, the requested
// mod last), then applies the new mod set.
func (s *Service) installRefs(ctx context.Context, g *catalog.Game, inst *store.Instance, provider string, refs []mods.Ref,
	r jobs.Report, download func(context.Context, mods.Ref, io.Writer) (mods.Artifact, error)) error {
	if len(refs) == 0 {
		return errors.New("provider returned nothing to install")
	}
	layout, err := mods.LayoutFor(g.Manifest.Mods.Loader)
	if err != nil {
		return err
	}
	current, err := s.store.ListMods(ctx, inst.ID)
	if err != nil {
		return err
	}
	root := refs[len(refs)-1]
	rootID := ""
	for _, m := range current {
		if m.Provider == provider && m.PackageID == root.ID {
			rootID = m.ID
		}
	}
	if rootID == "" {
		rootID = newID()
	}

	for i, ref := range refs {
		if !validPackageID(ref.ID) {
			return fmt.Errorf("provider returned invalid package id %q", ref.ID)
		}
		isRoot := i == len(refs)-1
		existing := slices.IndexFunc(current, func(m *store.Mod) bool { return m.Provider == provider && m.PackageID == ref.ID })
		if existing >= 0 && (!isRoot || current[existing].Version == ref.Version) {
			m := current[existing]
			if isRoot {
				m.Explicit = true
			} else if !slices.Contains(m.RequiredBy, rootID) && m.ID != rootID {
				m.RequiredBy = append(m.RequiredBy, rootID)
			}
			continue
		}

		r(0.1+0.7*float64(i)/float64(len(refs)), fmt.Sprintf("downloading %s %s", ref.ID, ref.Version))
		sum, err := s.downloadAndStage(ctx, inst.ID, provider, ref, layout, download)
		if err != nil {
			return fmt.Errorf("%s %s: %w", ref.ID, ref.Version, err)
		}
		m := &store.Mod{Provider: provider, PackageID: ref.ID, Version: ref.Version, SHA256: sum, InstalledAt: time.Now().UTC()}
		if existing >= 0 { // upgrade in place, keep identity and relations
			old := current[existing]
			m.ID, m.Explicit, m.RequiredBy = old.ID, old.Explicit || isRoot, old.RequiredBy
			current[existing] = m
			continue
		}
		if isRoot {
			m.ID, m.Explicit, m.RequiredBy = rootID, true, []string{}
		} else {
			m.ID, m.RequiredBy = newID(), []string{rootID}
		}
		current = append(current, m)
	}
	r(0.9, "applying mods")
	return s.commitMods(ctx, g, inst, current)
}

func (s *Service) downloadAndStage(ctx context.Context, id, provider string, ref mods.Ref, layout mods.Layout,
	download func(context.Context, mods.Ref, io.Writer) (mods.Artifact, error)) (string, error) {
	dl := filepath.Join(s.dir(id), "data", "mods", ".downloads")
	if err := os.MkdirAll(dl, 0o775); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dl, "dl-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	h := sha256.New()
	art, err := download(ctx, ref, io.MultiWriter(f, h))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if art.SHA256 != "" && !strings.EqualFold(art.SHA256, sum) {
		return "", fmt.Errorf("checksum mismatch: provider says %s, downloaded %s", art.SHA256, sum)
	}
	dir := s.stageDir(id, provider, ref)
	os.RemoveAll(dir)
	if err := os.MkdirAll(filepath.Dir(dir), 0o775); err != nil {
		return "", err
	}
	if err := mods.Stage(f.Name(), dir, ref.ID, layout); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	return sum, nil
}

// commitMods applies the mod set to the install, then records it and drops unused stages.
func (s *Service) commitMods(ctx context.Context, g *catalog.Game, inst *store.Instance, set []*store.Mod) error {
	if err := s.applyMods(g, inst.ID, set); err != nil {
		return err
	}
	if err := s.store.ReplaceMods(ctx, inst.ID, set); err != nil {
		return err
	}
	s.pruneStages(inst.ID, set)
	return nil
}

// applyMods syncs the install dir with the staged mods: loader packs first, then by
// install time.
func (s *Service) applyMods(g *catalog.Game, id string, set []*store.Mod) error {
	ordered := slices.Clone(set)
	sort.SliceStable(ordered, func(i, j int) bool {
		li, lj := mods.IsBepInExPack(ordered[i].PackageID), mods.IsBepInExPack(ordered[j].PackageID)
		if li != lj {
			return li
		}
		return ordered[i].InstalledAt.Before(ordered[j].InstalledAt)
	})
	stages := make([]string, 0, len(ordered))
	for _, m := range ordered {
		stages = append(stages, s.stageDir(id, m.Provider, mods.Ref{ID: m.PackageID, Version: m.Version}))
	}
	record := filepath.Join(s.dir(id), "data", renderedDir, "mods-applied.json")
	return mods.Apply(s.serverDir(g.Manifest.ID, id), record, stages)
}

// reapplyMods restores mod files after a game update (steamcmd validate may replace them).
func (s *Service) reapplyMods(ctx context.Context, g *catalog.Game, id string) error {
	set, err := s.store.ListMods(ctx, id)
	if err != nil || len(set) == 0 {
		return err
	}
	return s.applyMods(g, id, set)
}

func (s *Service) pruneStages(id string, set []*store.Mod) {
	keep := map[string]bool{}
	for _, m := range set {
		keep[s.stageDir(id, m.Provider, mods.Ref{ID: m.PackageID, Version: m.Version})] = true
	}
	root := filepath.Join(s.dir(id), "data", "mods")
	provs, _ := os.ReadDir(root)
	for _, p := range provs {
		if !p.IsDir() || strings.HasPrefix(p.Name(), ".") {
			continue
		}
		pkgs, _ := os.ReadDir(filepath.Join(root, p.Name()))
		for _, d := range pkgs {
			if dir := filepath.Join(root, p.Name(), d.Name()); !keep[dir] {
				os.RemoveAll(dir)
			}
		}
	}
}

func (s *Service) stageDir(id, provider string, ref mods.Ref) string {
	return filepath.Join(s.dir(id), "data", "mods", provider, ref.ID+"@"+safeSegment(ref.Version))
}

// modTarget checks the instance can take mod changes.
func (s *Service) modTarget(ctx context.Context, id string) (*store.Instance, *catalog.Game, error) {
	inst, err := s.store.GetInstance(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	g := s.games[inst.GameID]
	if g == nil {
		return nil, nil, fmt.Errorf("game %q no longer available", inst.GameID)
	}
	if g.Manifest.Mods.Loader == "none" {
		return nil, nil, fmt.Errorf("%w: %s does not support mods", ErrInvalid, g.Manifest.Name)
	}
	return inst, g, nil
}

// modJob runs fn as a job while the instance is held in `updating`. Mods change only
// while the server is stopped and installed.
func (s *Service) modJob(ctx context.Context, inst *store.Instance, kind string, fn func(context.Context, jobs.Report) error) (*jobs.Job, error) {
	unlock := s.lock(inst.ID)
	err := s.store.Transition(ctx, inst.ID, []string{store.StateStopped, store.StateError}, store.StateUpdating, "changing mods")
	unlock()
	if err != nil {
		return nil, fmt.Errorf("%w (mods can only change while the server is stopped)", err)
	}
	return s.jobs.Start(inst.ID, kind, func(ctx context.Context, r jobs.Report) error {
		return s.finish(inst.ID, store.StateUpdating, store.StateStopped, fn(ctx, r))
	}), nil
}

func (s *Service) provider(g *catalog.Game, name string) (mods.Provider, string, error) {
	for _, p := range g.Manifest.Mods.Providers {
		if p.Type != name {
			continue
		}
		impl := s.opt.ModProviders[name]
		if impl == nil {
			return nil, "", fmt.Errorf("%w: mod provider %q is not available", ErrInvalid, name)
		}
		scope := p.Community
		if p.Type == "nexus" {
			scope = p.GameDomain
		}
		return impl, scope, nil
	}
	return nil, "", fmt.Errorf("%w: %s has no %q mods", ErrInvalid, g.Manifest.Name, name)
}

func (s *Service) spool(id string, r io.Reader) (string, error) {
	dir := filepath.Join(s.dir(id), "data", "mods", ".downloads")
	if err := os.MkdirAll(dir, 0o775); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, "upload-*")
	if err != nil {
		return "", err
	}
	_, err = io.Copy(f, r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// validPackageID accepts provider ids that are safe as a single path segment
// (Thunderstore "Namespace-Name", Nexus "<modId>:<fileId>").
func validPackageID(id string) bool {
	if id == "" || len(id) > 200 || id == "." || id == ".." {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-_.:", c)) {
			return false
		}
	}
	return true
}

func safeSegment(v string) string {
	return strings.Map(func(c rune) rune {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '-' || c == '_' {
			return c
		}
		return '_'
	}, v)
}

func newID() string { return strings.ToLower(rand.Text()[:12]) }
