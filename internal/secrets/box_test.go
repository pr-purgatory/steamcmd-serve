package secrets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBox(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.key")
	b, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("key mode %v", fi.Mode().Perm())
	}
	sealed := b.Seal("hunter22")
	if !strings.HasPrefix(sealed, prefix) || strings.Contains(sealed, "hunter22") || sealed == b.Seal("hunter22") {
		t.Fatalf("sealed = %q (must be prefixed, opaque, randomized)", sealed)
	}
	b2, err := Open(path) // reload same key
	if err != nil {
		t.Fatal(err)
	}
	if got, err := b2.Open(sealed); err != nil || got != "hunter22" {
		t.Fatalf("open = %q, %v", got, err)
	}
	if got, _ := b2.Open("legacy-plain"); got != "legacy-plain" {
		t.Fatal("unprefixed values must pass through")
	}
	other, _ := New(make([]byte, 32))
	if _, err := other.Open(sealed); err == nil {
		t.Fatal("wrong key must fail")
	}
	if b.Seal("") != "" {
		t.Fatal("empty must stay empty")
	}
}
