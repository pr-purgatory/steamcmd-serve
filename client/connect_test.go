package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestNormalizeURL(t *testing.T) {
	ok := map[string]string{
		"192.168.1.50:8080":       "http://192.168.1.50:8080",
		"  http://h:8080/ ":        "http://h:8080",
		"https://ctl.example.com":  "https://ctl.example.com",
		"https://ctl.example.com/": "https://ctl.example.com",
		"localhost:8080":           "http://localhost:8080",
		"http://[::1]:8080":        "http://[::1]:8080",
		"HTTP://Host.Local":        "http://Host.Local",
	}
	for in, want := range ok {
		got, err := normalizeURL(in)
		if err != nil || got != want {
			t.Errorf("normalizeURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	bad := []string{"", "   ", "ftp://x", "http://user:pass@h", "http://user@h", "http://h/path",
		"http://h/a/", "http://h?x=1", "http://h/#f", "http://", "://", "http://h:port", "ht tp://x", "%%%", "file:///etc/passwd", "javascript:alert(1)"}
	for _, in := range bad {
		if got, err := normalizeURL(in); err == nil {
			t.Errorf("normalizeURL(%q) = %q; want error", in, got)
		}
	}
}

func TestCheckHealth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path != "/healthz":
			w.WriteHeader(404)
		default:
			w.WriteHeader(204)
		}
	}))
	defer srv.Close()
	if err := checkHealth(srv.URL, time.Second); err != nil {
		t.Fatalf("204: %v", err)
	}

	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("hi")) }))
	defer web.Close()
	err := checkHealth(web.URL, time.Second)
	if err == nil || !strings.Contains(err.Error(), "not a steamcmd-serve controller") {
		t.Fatalf("200: got %v", err)
	}

	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/login", http.StatusFound)
	}))
	defer redir.Close()
	if err := checkHealth(redir.URL, time.Second); err == nil {
		t.Fatal("redirect should not count as healthy")
	}

	done := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
		w.WriteHeader(204)
	}))
	defer slow.Close()
	defer close(done)
	err = checkHealth(slow.URL, 100*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("timeout: got %v", err)
	}

	if err := checkHealth("http://127.0.0.1:1", time.Second); err == nil {
		t.Fatal("refused connection should error")
	}
}

func TestConfigRoundTrip(t *testing.T) {
	base := t.TempDir()
	s := configStore{dirFunc: func() (string, error) { return base, nil }}
	if got := s.load(); got != "" {
		t.Fatalf("empty load = %q", got)
	}
	if err := s.save("http://192.168.1.50:8080"); err != nil {
		t.Fatal(err)
	}
	if err := s.save("http://10.0.0.5:8080"); err != nil { // overwrite
		t.Fatal(err)
	}
	if got := s.load(); got != "http://10.0.0.5:8080" {
		t.Fatalf("load = %q", got)
	}
	p := filepath.Join(base, "steamcmd-serve-client", "config.json")
	raw, _ := os.ReadFile(p)
	if string(raw) != `{"controllerURL":"http://10.0.0.5:8080"}` {
		t.Fatalf("file = %s", raw)
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(p)
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("file mode %v", fi.Mode().Perm())
		}
		di, _ := os.Stat(filepath.Dir(p))
		if di.Mode().Perm() != 0o700 {
			t.Errorf("dir mode %v", di.Mode().Perm())
		}
	}
	os.WriteFile(p, []byte("not json"), 0o600)
	if got := s.load(); got != "" {
		t.Fatalf("corrupt load = %q", got)
	}
}

func TestIsPrivateHost(t *testing.T) {
	for _, h := range []string{"10.1.2.3", "172.16.0.1", "172.31.255.255", "192.168.1.50", "127.0.0.1",
		"::1", "[::1]", "fc00::1", "fd12:3456::1", "localhost", "LOCALHOST", "nas.local", "nas.local."} {
		if !isPrivateHost(h) {
			t.Errorf("%q should be private", h)
		}
	}
	for _, h := range []string{"8.8.8.8", "172.32.0.1", "172.15.0.1", "192.169.0.1", "11.0.0.1",
		"2001:db8::1", "example.com", "localhost.example.com", "mylocal"} {
		if isPrivateHost(h) {
			t.Errorf("%q should be public", h)
		}
	}
	if !insecureRemote("http://example.com") || insecureRemote("https://example.com") || insecureRemote("http://192.168.1.1:80") {
		t.Error("insecureRemote wrong")
	}
}

func TestUnreachableHint(t *testing.T) {
	wrapped := fmt.Errorf("dial tcp 192.168.1.5:8080: %w", syscall.EHOSTUNREACH)
	if h := unreachableHint("darwin", wrapped); !strings.Contains(h, "Local Network") {
		t.Errorf("darwin EHOSTUNREACH: no hint (%q)", h)
	}
	if h := unreachableHint("linux", wrapped); h != "" {
		t.Errorf("linux: unexpected hint %q", h)
	}
	if h := unreachableHint("darwin", syscall.ECONNREFUSED); h != "" {
		t.Errorf("connection refused: unexpected hint %q", h)
	}
}
