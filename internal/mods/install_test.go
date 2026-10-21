package mods

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeZip(t *testing.T, dir, name string, files map[string]string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for n, body := range files {
		w, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	zw.Close()
	f.Close()
	return p
}

func TestBepInExLayout(t *testing.T) {
	cases := []struct {
		pkg, entry, dest string
		user, ok         bool
	}{
		{"denikson-BepInExPack_Valheim", "BepInExPack_Valheim/BepInEx/core/BepInEx.dll", "BepInEx/core/BepInEx.dll", false, true},
		{"denikson-BepInExPack_Valheim", "BepInExPack_Valheim/libdoorstop_x64.so", "libdoorstop_x64.so", false, true},
		{"denikson-BepInExPack_Valheim", "manifest.json", "", false, false},
		{"A-Mod", "manifest.json", "", false, false},
		{"A-Mod", "README.md", "", false, false},
		{"A-Mod", "Mod.dll", "BepInEx/plugins/A-Mod/Mod.dll", false, true},
		{"A-Mod", "plugins/Mod.dll", "BepInEx/plugins/A-Mod/Mod.dll", false, true},
		{"A-Mod", "patchers/P.dll", "BepInEx/patchers/A-Mod/P.dll", false, true},
		{"A-Mod", "config/a.cfg", "BepInEx/config/a.cfg", true, true},
		{"A-Mod", "BepInEx/plugins/X/x.dll", "BepInEx/plugins/X/x.dll", false, true},
		{"A-Mod", "BepInEx/config/x.cfg", "BepInEx/config/x.cfg", true, true},
	}
	for _, c := range cases {
		dest, user, ok := BepInEx(c.pkg, c.entry)
		if dest != c.dest || user != c.user || ok != c.ok {
			t.Errorf("%s %s: got (%q,%v,%v) want (%q,%v,%v)", c.pkg, c.entry, dest, user, ok, c.dest, c.user, c.ok)
		}
	}
}

func TestStageRejectsHostileArchives(t *testing.T) {
	dir := t.TempDir()
	for name, files := range map[string]map[string]string{
		"slip.zip": {"../../evil.dll": "x"},
		"abs.zip":  {"/etc/passwd": "x"},
	} {
		z := writeZip(t, dir, name, files)
		if err := Stage(z, filepath.Join(dir, name+".stage"), "A-Mod", BepInEx); err == nil || !strings.Contains(err.Error(), "escapes") {
			t.Errorf("%s: want escape error, got %v", name, err)
		}
	}
	notZip := filepath.Join(dir, "mod.7z")
	os.WriteFile(notZip, []byte("7z\xbc\xaf"), 0o644)
	if err := Stage(notZip, filepath.Join(dir, "7z.stage"), "A-Mod", BepInEx); err == nil || !strings.Contains(err.Error(), "only .zip") {
		t.Errorf("non-zip: %v", err)
	}
}

func TestStageAndApply(t *testing.T) {
	dir := t.TempDir()
	install := filepath.Join(dir, "server")
	os.MkdirAll(install, 0o755)
	os.WriteFile(filepath.Join(install, "valheim_server.x86_64"), []byte("game"), 0o755)
	record := filepath.Join(dir, "applied.json")

	stage := func(pkg string, files map[string]string) string {
		s := filepath.Join(dir, "stage-"+pkg)
		if err := Stage(writeZip(t, dir, pkg+".zip", files), s, pkg, BepInEx); err != nil {
			t.Fatal(err)
		}
		return s
	}
	loader := stage("denikson-BepInExPack_Valheim", map[string]string{
		"manifest.json": "{}",
		"BepInExPack_Valheim/BepInEx/core/BepInEx.dll":   "core",
		"BepInExPack_Valheim/libdoorstop_x64.so":         "doorstop",
		"BepInExPack_Valheim/BepInEx/config/BepInEx.cfg": "default-cfg",
	})
	modA := stage("A-Mod", map[string]string{"plugins/A.dll": "a1", "config/a.cfg": "a-default", "icon.png": "png"})
	exists := func(rel string) bool {
		_, err := os.Stat(filepath.Join(install, rel))
		return err == nil
	}
	read := func(rel string) string {
		b, _ := os.ReadFile(filepath.Join(install, rel))
		return string(b)
	}

	if err := Apply(install, record, []string{loader, modA}); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"BepInEx/core/BepInEx.dll", "libdoorstop_x64.so", "BepInEx/plugins/A-Mod/A.dll", "BepInEx/config/a.cfg"} {
		if !exists(rel) {
			t.Errorf("missing %s after apply", rel)
		}
	}
	if exists("icon.png") || exists("BepInEx/plugins/A-Mod/icon.png") {
		t.Error("meta files must not be installed")
	}

	// User edits a config; re-apply keeps the edit.
	os.WriteFile(filepath.Join(install, "BepInEx/config/a.cfg"), []byte("edited"), 0o644)
	if err := Apply(install, record, []string{loader, modA}); err != nil {
		t.Fatal(err)
	}
	if read("BepInEx/config/a.cfg") != "edited" {
		t.Error("re-apply overwrote a user-edited config")
	}

	// Uninstall A: its plugin goes, the loader stays, configs are kept.
	if err := Apply(install, record, []string{loader}); err != nil {
		t.Fatal(err)
	}
	if exists("BepInEx/plugins/A-Mod") || !exists("BepInEx/core/BepInEx.dll") || !exists("BepInEx/config/a.cfg") {
		t.Error("uninstall did not remove exactly the mod's files")
	}

	// Remove everything: back to the game files only (plus user configs).
	if err := Apply(install, record, nil); err != nil {
		t.Fatal(err)
	}
	if exists("libdoorstop_x64.so") || exists("BepInEx/core") || !exists("valheim_server.x86_64") {
		t.Error("removing all mods did not restore the install")
	}

	// A mod that would overwrite a game file is refused.
	over := filepath.Join(dir, "stage-over")
	os.MkdirAll(over, 0o755)
	os.WriteFile(filepath.Join(over, "valheim_server.x86_64"), []byte("evil"), 0o644)
	if err := Apply(install, record, []string{over}); err == nil || !strings.Contains(err.Error(), "overwrite an existing game file") {
		t.Fatalf("game file overwrite: %v", err)
	}
	if read("valheim_server.x86_64") != "game" {
		t.Fatal("game file was modified")
	}
}
