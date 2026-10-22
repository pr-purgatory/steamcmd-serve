package mods

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Limits against hostile archives (zip bombs).
const (
	maxEntries    = 20000
	maxTotalBytes = 2 << 30 // 2 GiB uncompressed
)

// Layout maps an archive entry (slash-separated, relative) to its destination
// relative to the game install dir. ok=false skips the entry. userFile marks files the
// user may edit afterwards (configs): they are copied only when missing and never removed.
type Layout func(pkgID, entry string) (dest string, userFile, ok bool)

// LayoutFor returns the layout for a manifest mods.loader value.
func LayoutFor(loader string) (Layout, error) {
	switch loader {
	case "bepinex":
		return BepInEx, nil
	default:
		return nil, fmt.Errorf("mods are not supported for loader %q", loader)
	}
}

// IsBepInExPack reports whether pkgID ("Namespace-Name") is a BepInEx loader pack.
func IsBepInExPack(pkgID string) bool {
	_, name, _ := strings.Cut(pkgID, "-")
	return strings.HasPrefix(name, "BepInExPack")
}

var metaFiles = map[string]bool{"manifest.json": true, "icon.png": true, "readme.md": true, "changelog.md": true, "license": true, "license.md": true, "license.txt": true}

// BepInEx follows the Thunderstore/r2modman conventions for BepInEx 5 mods:
// the loader pack's inner folder goes to the install root; plugins/, patchers/ and
// loose files go under BepInEx/<kind>/<package>/; config/ goes to BepInEx/config/;
// archives already rooted at BepInEx/ keep their paths.
func BepInEx(pkgID, entry string) (string, bool, bool) {
	entry = strings.TrimPrefix(entry, "./")
	if IsBepInExPack(pkgID) {
		_, name, _ := strings.Cut(pkgID, "-")
		rest, ok := strings.CutPrefix(entry, name+"/")
		if !ok || rest == "" {
			return "", false, false
		}
		return rest, false, true
	}
	if !strings.Contains(entry, "/") && metaFiles[strings.ToLower(entry)] {
		return "", false, false
	}
	first, rest, nested := strings.Cut(entry, "/")
	switch {
	case nested && strings.EqualFold(first, "BepInEx"):
		return entry, strings.HasPrefix(strings.ToLower(rest), "config/"), true
	case nested && strings.EqualFold(first, "plugins"):
		return path.Join("BepInEx/plugins", pkgID, rest), false, true
	case nested && strings.EqualFold(first, "patchers"):
		return path.Join("BepInEx/patchers", pkgID, rest), false, true
	case nested && strings.EqualFold(first, "config"):
		return path.Join("BepInEx/config", rest), true, true
	default:
		return path.Join("BepInEx/plugins", pkgID, entry), false, true
	}
}

// Stage extracts archive into dir (which must not exist) using layout, so dir holds the
// files exactly as they will appear in the install. User files go under dir/.user/.
// Entries escaping the archive root, links and other special files are rejected.
func Stage(archive, dir, pkgID string, layout Layout) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return fmt.Errorf("open archive (only .zip is supported): %w", err)
	}
	defer zr.Close()
	if len(zr.File) > maxEntries {
		return fmt.Errorf("archive has %d entries (limit %d)", len(zr.File), maxEntries)
	}
	if err := os.Mkdir(dir, 0o775); err != nil {
		return err
	}
	var total int64
	for _, f := range zr.File {
		name := strings.ReplaceAll(f.Name, `\`, "/")
		if strings.HasSuffix(name, "/") {
			continue
		}
		if !filepath.IsLocal(filepath.FromSlash(name)) {
			return fmt.Errorf("archive entry %q escapes the archive", f.Name)
		}
		if !f.Mode().IsRegular() {
			return fmt.Errorf("archive entry %q is not a regular file", f.Name)
		}
		dest, user, ok := layout(pkgID, name)
		if !ok {
			continue
		}
		if !filepath.IsLocal(filepath.FromSlash(dest)) {
			return fmt.Errorf("archive entry %q maps outside the install", f.Name)
		}
		if user {
			dest = path.Join(".user", dest)
		}
		total += int64(f.UncompressedSize64)
		if total > maxTotalBytes {
			return fmt.Errorf("archive expands beyond %d bytes", int64(maxTotalBytes))
		}
		if err := extractFile(f, filepath.Join(dir, filepath.FromSlash(dest)), maxTotalBytes); err != nil {
			return err
		}
	}
	return nil
}

func extractFile(f *zip.File, target string, limit int64) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o775); err != nil {
		return err
	}
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o664)
	if err != nil {
		return err
	}
	n, err := io.Copy(out, io.LimitReader(rc, limit+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > limit {
		err = errors.New("archive entry larger than declared")
	}
	return err
}

// Apply makes the mod files in installDir match the staged dirs (in order; later dirs
// win on conflicts). Files written by the previous Apply (listed in record) are removed
// first, so the result never depends on history. Existing game files are never
// overwritten. User files are copied only when missing and are never removed.
// Apply with no stages restores the install to its unmodded state.
func Apply(installDir, record string, stages []string) error {
	prev, err := readRecord(record)
	if err != nil {
		return err
	}
	for _, rel := range prev {
		p := filepath.Join(installDir, filepath.FromSlash(rel))
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		removeEmptyParents(installDir, filepath.Dir(p))
	}

	written := map[string]bool{}
	for _, stage := range stages {
		err := filepath.WalkDir(stage, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			rel, _ := filepath.Rel(stage, p)
			rel = filepath.ToSlash(rel)
			if userRel, ok := strings.CutPrefix(rel, ".user/"); ok {
				target := filepath.Join(installDir, filepath.FromSlash(userRel))
				if _, err := os.Stat(target); err == nil {
					return nil
				}
				return copyFile(p, target)
			}
			target := filepath.Join(installDir, filepath.FromSlash(rel))
			if !written[rel] {
				if _, err := os.Lstat(target); err == nil {
					return fmt.Errorf("mod file %s would overwrite an existing game file", rel)
				}
			}
			written[rel] = true
			return copyFile(p, target)
		})
		if err != nil {
			// Record what was written so far; the next Apply cleans it up.
			writeRecord(record, written)
			return err
		}
	}
	return writeRecord(record, written)
}

func readRecord(record string) ([]string, error) {
	for _, seg := range strings.Split(filepath.ToSlash(record), "/") {
		if seg == ".." {
			return nil, fmt.Errorf("invalid file path")
		}
	}
	raw, err := os.ReadFile(record)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var files []string
	if err := json.Unmarshal(raw, &files); err != nil {
		return nil, fmt.Errorf("corrupt mod record %s: %w", record, err)
	}
	for _, f := range files {
		if !filepath.IsLocal(filepath.FromSlash(f)) {
			return nil, fmt.Errorf("corrupt mod record %s: %q", record, f)
		}
	}
	return files, nil
}

func writeRecord(record string, written map[string]bool) error {
	files := make([]string, 0, len(written))
	for f := range written {
		files = append(files, f)
	}
	sort.Strings(files)
	raw, err := json.Marshal(files)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(record), 0o775); err != nil {
		return err
	}
	tmp := record + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o664); err != nil {
		return err
	}
	return os.Rename(tmp, record)
}

func copyFile(src, dst string) error {
	for _, seg := range strings.Split(filepath.ToSlash(src), "/") {
		if seg == ".." {
			return fmt.Errorf("invalid file path")
		}
	}
	for _, seg := range strings.Split(filepath.ToSlash(dst), "/") {
		if seg == ".." {
			return fmt.Errorf("invalid file path")
		}
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o775); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fi.Mode().Perm()|0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

// removeEmptyParents removes now-empty directories from dir up to (not including) root.
func removeEmptyParents(root, dir string) {
	for dir != root && strings.HasPrefix(dir, root+string(filepath.Separator)) {
		if os.Remove(dir) != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}
