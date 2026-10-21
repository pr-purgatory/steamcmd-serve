package auth

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"steamcmd-serve/internal/store"
)

func newServer(t *testing.T, trusted ...string) (*httptest.Server, *store.Store, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "c.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	a, err := New(st, trusted)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	a.Register(mux)
	mux.HandleFunc("GET /api/v1/instances", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(Username(r.Context())))
	})
	mux.HandleFunc("POST /api/v1/instances", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(201) })
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ui")) })
	srv := httptest.NewServer(a.Middleware(mux))
	t.Cleanup(srv.Close)
	return srv, st, dbPath
}

type client struct {
	t    *testing.T
	http *http.Client
	base string
}

func newClient(t *testing.T, base string) *client {
	jar, _ := cookiejar.New(nil)
	return &client{t: t, http: &http.Client{Jar: jar}, base: base}
}

func (c *client) do(method, path string, body any, hdr map[string]string) (*http.Response, string) {
	c.t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, c.base+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	buf.ReadFrom(resp.Body)
	return resp, buf.String()
}

func TestAuthFlow(t *testing.T) {
	srv, _, dbPath := newServer(t)
	c := newClient(t, srv.URL)
	creds := map[string]string{"username": "admin", "password": "correct horse battery"}

	if _, body := c.do("GET", "/api/v1/setup", nil, nil); !strings.Contains(body, `"required":true`) {
		t.Fatalf("setup status before: %s", body)
	}
	if r, _ := c.do("GET", "/", nil, nil); r.StatusCode != 200 {
		t.Fatalf("UI must be public, got %d", r.StatusCode)
	}
	if r, _ := c.do("GET", "/api/v1/instances", nil, nil); r.StatusCode != 401 {
		t.Fatalf("API without session: %d", r.StatusCode)
	}
	if r, _ := c.do("POST", "/api/v1/setup", map[string]string{"username": "admin", "password": "short"}, nil); r.StatusCode != 400 {
		t.Fatalf("short password: %d", r.StatusCode)
	}
	if r, _ := c.do("POST", "/api/v1/setup", creds, nil); r.StatusCode != 201 {
		t.Fatalf("setup: %d", r.StatusCode)
	}
	if r, _ := c.do("POST", "/api/v1/setup", map[string]string{"username": "evil", "password": "another long password"}, nil); r.StatusCode != 409 {
		t.Fatalf("second setup must be locked: %d", r.StatusCode)
	}
	if _, body := c.do("GET", "/api/v1/setup", nil, nil); !strings.Contains(body, `"required":false`) {
		t.Fatalf("setup status after: %s", body)
	}

	// Login CSRF guard: a form post is rejected.
	req, _ := http.NewRequest("POST", srv.URL+"/api/v1/auth/login", strings.NewReader("username=admin&password=x"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if r, _ := c.http.Do(req); r.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("form login: %d", r.StatusCode)
	}

	if r, _ := c.do("POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": "wrong password!!"}, nil); r.StatusCode != 401 {
		t.Fatalf("wrong password: %d", r.StatusCode)
	}
	r, _ := c.do("POST", "/api/v1/auth/login", map[string]string{"username": "ADMIN", "password": creds["password"]}, nil)
	if r.StatusCode != 204 {
		t.Fatalf("login: %d", r.StatusCode)
	}
	var cookie *http.Cookie
	for _, ck := range r.Cookies() {
		if ck.Name == CookieName {
			cookie = ck
		}
	}
	if cookie == nil || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Secure {
		t.Fatalf("cookie = %+v (HttpOnly, Lax, not Secure over plain http)", cookie)
	}

	_, body := c.do("GET", "/api/v1/auth/session", nil, nil)
	var sess struct{ Username, CsrfToken string }
	json.Unmarshal([]byte(body), &sess)
	if sess.Username != "admin" || sess.CsrfToken == "" {
		t.Fatalf("session = %s", body)
	}
	if _, body := c.do("GET", "/api/v1/instances", nil, nil); body != "admin" {
		t.Fatalf("authenticated GET: %q", body)
	}
	if r, _ := c.do("POST", "/api/v1/instances", map[string]any{}, nil); r.StatusCode != 403 {
		t.Fatalf("POST without CSRF: %d", r.StatusCode)
	}
	if r, _ := c.do("POST", "/api/v1/instances", map[string]any{}, map[string]string{CSRFHeader: "nope"}); r.StatusCode != 403 {
		t.Fatalf("POST wrong CSRF: %d", r.StatusCode)
	}
	if r, _ := c.do("POST", "/api/v1/instances", map[string]any{}, map[string]string{CSRFHeader: sess.CsrfToken}); r.StatusCode != 201 {
		t.Fatalf("POST with CSRF: %d", r.StatusCode)
	}

	// The raw cookie token is never stored, and the password is hashed.
	db, _ := os.ReadFile(dbPath)
	wal, _ := os.ReadFile(dbPath + "-wal")
	all := append(db, wal...)
	if bytes.Contains(all, []byte(cookie.Value)) || bytes.Contains(all, []byte(creds["password"])) {
		t.Fatal("session token or password stored in plaintext")
	}

	if r, _ := c.do("POST", "/api/v1/auth/logout", nil, map[string]string{CSRFHeader: sess.CsrfToken}); r.StatusCode != 204 {
		t.Fatalf("logout: %d", r.StatusCode)
	}
	// Replaying the old cookie after logout fails.
	req, _ = http.NewRequest("GET", srv.URL+"/api/v1/instances", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: cookie.Value})
	if r, _ := http.DefaultClient.Do(req); r.StatusCode != 401 {
		t.Fatalf("replayed cookie after logout: %d", r.StatusCode)
	}
}

func TestLoginRateLimit(t *testing.T) {
	srv, _, _ := newServer(t)
	c := newClient(t, srv.URL)
	c.do("POST", "/api/v1/setup", map[string]string{"username": "admin", "password": "correct horse battery"}, nil)
	for i := 0; i < maxFailures; i++ {
		if r, _ := c.do("POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": "wrong password!!"}, nil); r.StatusCode != 401 {
			t.Fatalf("attempt %d: %d", i, r.StatusCode)
		}
	}
	if r, _ := c.do("POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": "correct horse battery"}, nil); r.StatusCode != 429 {
		t.Fatalf("after %d failures even the right password is rate limited, got %d", maxFailures, r.StatusCode)
	}
}

func TestForwardedHeaders(t *testing.T) {
	a, _ := New(nil, []string{"10.0.0.0/8"})
	r := httptest.NewRequest("POST", "/", nil)
	r.RemoteAddr = "10.1.2.3:5555"
	r.Header.Set("X-Forwarded-For", "203.0.113.9, 10.9.9.9")
	r.Header.Set("X-Forwarded-Proto", "https")
	if got := a.clientIP(r); got != "203.0.113.9" || !a.isHTTPS(r) {
		t.Fatalf("trusted proxy: ip %s https %v", got, a.isHTTPS(r))
	}
	r.RemoteAddr = "198.51.100.7:5555" // untrusted: headers ignored
	if got := a.clientIP(r); got != "198.51.100.7" || a.isHTTPS(r) {
		t.Fatalf("untrusted: ip %s https %v", got, a.isHTTPS(r))
	}
}
