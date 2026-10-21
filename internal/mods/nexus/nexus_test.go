package nexus

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"steamcmd-serve/internal/mods"
	"steamcmd-serve/internal/secrets"
	"steamcmd-serve/internal/store"
)

const testKey = "test-key-not-real"

// fakeNexus emulates the Nexus REST + GraphQL + CDN endpoints.
type fakeNexus struct {
	t       *testing.T
	srv     *httptest.Server
	mu      sync.Mutex
	premium bool
	files   string // files.json body
	status  map[string]int
	cdn     string // URI returned by download_link.json
	payload []byte
	gqlBody string
	cdnKey  string // apikey header seen by the CDN endpoint
}

func newFake(t *testing.T) *fakeNexus {
	f := &fakeNexus{t: t, premium: true, status: map[string]int{}, payload: []byte("PK-zip-bytes")}
	f.files = `{"files":[
	 {"file_id":1,"name":"old","version":"1.0","category_name":"MAIN","is_primary":false,"file_name":"old.zip","uploaded_timestamp":100},
	 {"file_id":2,"name":"new","version":"2.0","category_name":"MAIN","is_primary":false,"file_name":"new.zip","uploaded_timestamp":300},
	 {"file_id":3,"name":"opt","version":"9.0","category_name":"OPTIONAL","is_primary":false,"file_name":"opt.zip","uploaded_timestamp":900}]}`
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/users/validate.json", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("apikey") != testKey {
			http.Error(w, `{"message":"Please provide a valid API Key"}`, 401)
			return
		}
		if r.Header.Get("Application-Name") != "steamcmd-serve" || r.Header.Get("User-Agent") == "" {
			t.Error("missing identification headers")
		}
		fmt.Fprintf(w, `{"user_id":1,"name":"tester","is_premium":%v}`, f.premium)
	})
	mux.HandleFunc("/v1/games/valheim/mods/10/files.json", func(w http.ResponseWriter, r *http.Request) {
		if c := f.status["files"]; c != 0 {
			w.WriteHeader(c)
			return
		}
		io.WriteString(w, f.files)
	})
	mux.HandleFunc("/v1/games/valheim/mods/10/files/2/download_link.json", func(w http.ResponseWriter, r *http.Request) {
		if !f.premium {
			http.Error(w, `{"message":"forbidden"}`, 403)
			return
		}
		fmt.Fprintf(w, `[{"name":"CDN","short_name":"CDN","URI":%q}]`, f.cdn)
	})
	mux.HandleFunc("/cdn/file", func(w http.ResponseWriter, r *http.Request) {
		f.cdnKey = r.Header.Get("apikey")
		w.Write(f.payload)
	})
	mux.HandleFunc("/v2/graphql", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.gqlBody = string(b)
		io.WriteString(w, `{"data":{"mods":{"nodes":[{"modId":4449,"name":"Epic Loot","summary":"s","author":"a","version":"1.2"}]}}}`)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	f.cdn = f.srv.URL + "/cdn/file"
	return f
}

func (f *fakeNexus) client() *Client {
	return &Client{HTTP: f.srv.Client(), RESTBase: f.srv.URL + "/v1", GraphQLURL: f.srv.URL + "/v2/graphql", Version: "test"}
}

func (f *fakeNexus) provider(key string) *Provider {
	p := NewProvider(func(context.Context) (string, error) {
		if key == "" {
			return "", ErrNoKey
		}
		return key, nil
	}, f.client())
	p.allowDownloadURL = func(u *url.URL) bool { return strings.HasPrefix(u.String(), f.srv.URL+"/cdn/") }
	return p
}

func newKeyring(t *testing.T, c *Client) (*Keyring, *store.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "t.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	box, err := secrets.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return NewKeyring(st, box, c), st, path
}

func TestKeyringSetValidatesAndSealsKey(t *testing.T) {
	ctx := context.Background()
	f := newFake(t)
	k, st, path := newKeyring(t, f.client())

	if _, err := k.Set(ctx, "wrong-key-not-real"); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("bad key: %v", err)
	}
	if _, err := st.GetSetting(ctx, settingKey); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("bad key must not be stored: %v", err)
	}
	if _, err := k.Key(ctx); !errors.Is(err, ErrNoKey) {
		t.Fatalf("Key unset: %v", err)
	}
	if acct, err := k.Status(ctx); err != nil || acct.HasValue {
		t.Fatalf("Status unset: %+v %v", acct, err)
	}

	acct, err := k.Set(ctx, testKey)
	if err != nil || acct != (Account{HasValue: true, Premium: true, Username: "tester"}) {
		t.Fatalf("Set: %+v %v", acct, err)
	}
	if got, err := k.Key(ctx); err != nil || got != testKey {
		t.Fatalf("Key roundtrip failed: %v", err)
	}
	raw, _ := st.GetSetting(ctx, settingKey)
	if !strings.HasPrefix(raw, "enc:v1:") || strings.Contains(raw, testKey) {
		t.Fatal("key not sealed in app_settings")
	}
	cached, _ := st.GetSetting(ctx, settingAccount)
	if strings.Contains(cached, testKey) {
		t.Fatal("account cache contains key")
	}
	st.Close() // checkpoint WAL
	matches, _ := filepath.Glob(path + "*")
	for _, m := range matches {
		b, _ := os.ReadFile(m)
		if bytes.Contains(b, []byte(testKey)) {
			t.Fatalf("plaintext key found in %s", filepath.Base(m))
		}
	}

	st2, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	box, _ := secrets.New(bytes.Repeat([]byte{7}, 32))
	if err := NewKeyring(st2, box, f.client()).Delete(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := NewKeyring(st2, box, f.client()).Key(ctx); !errors.Is(err, ErrNoKey) {
		t.Fatalf("after delete: %v", err)
	}
}

func TestKeyringStatusOfflineFallback(t *testing.T) {
	ctx := context.Background()
	f := newFake(t)
	k, _, _ := newKeyring(t, f.client())
	if _, err := k.Set(ctx, testKey); err != nil {
		t.Fatal(err)
	}
	acct, err := k.Status(ctx)
	if err != nil || !acct.Premium {
		t.Fatalf("online status: %+v %v", acct, err)
	}
	f.srv.Close() // Nexus unreachable
	acct, err = k.Status(ctx)
	if err != nil || acct != (Account{HasValue: true, Premium: true, Username: "tester"}) {
		t.Fatalf("offline status: %+v %v", acct, err)
	}
}

func TestKeyringSetRateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(429) }))
	defer srv.Close()
	k, st, _ := newKeyring(t, &Client{HTTP: srv.Client(), RESTBase: srv.URL})
	if _, err := k.Set(context.Background(), testKey); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("got %v", err)
	}
	if _, err := st.GetSetting(context.Background(), settingKey); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("stored despite failure")
	}
}

func TestSearch(t *testing.T) {
	f := newFake(t)
	pkgs, err := f.provider(testKey).Search(context.Background(), "valheim", "epic")
	if err != nil {
		t.Fatal(err)
	}
	want := mods.Package{Ref: mods.Ref{ID: "4449", Version: "1.2"}, Name: "Epic Loot", Author: "a", Summary: "s", Dependencies: []mods.Ref{}}
	if len(pkgs) != 1 || fmt.Sprint(pkgs[0]) != fmt.Sprint(want) {
		t.Fatalf("got %+v", pkgs)
	}
	var req struct {
		Variables map[string]string `json:"variables"`
	}
	json.Unmarshal([]byte(f.gqlBody), &req)
	if req.Variables["domain"] != "valheim" || req.Variables["q"] != "epic" {
		t.Fatalf("variables: %v", req.Variables)
	}
	if _, err := f.provider(testKey).Search(context.Background(), "valheim", ""); err != nil {
		t.Fatalf("empty query: %v", err)
	}
	if strings.Contains(f.gqlBody, "WILDCARD") {
		t.Fatal("empty query must not filter by name")
	}
}

func TestNoKey(t *testing.T) {
	f := newFake(t)
	p := f.provider("")
	ctx := context.Background()
	_, e1 := p.Search(ctx, "valheim", "x")
	_, e2 := p.Resolve(ctx, "valheim", mods.Ref{ID: "10"})
	_, e3 := p.Download(ctx, "valheim", mods.Ref{ID: "10:2"}, io.Discard)
	for _, err := range []error{e1, e2, e3} {
		if !errors.Is(err, ErrNoKey) || !strings.Contains(err.Error(), "add a NexusMods API key in Settings") {
			t.Fatalf("got %v", err)
		}
	}
}

func TestResolve(t *testing.T) {
	f := newFake(t)
	p := f.provider(testKey)
	ctx := context.Background()

	got, err := p.Resolve(ctx, "valheim", mods.Ref{ID: "10"})
	if err != nil || len(got) != 1 || got[0] != (mods.Ref{ID: "10:2", Version: "2.0"}) {
		t.Fatalf("newest MAIN: %+v %v", got, err)
	}
	f.files = strings.Replace(f.files, `"is_primary":false,"file_name":"old.zip"`, `"is_primary":true,"file_name":"old.zip"`, 1)
	got, err = p.Resolve(ctx, "valheim", mods.Ref{ID: "10"})
	if err != nil || got[0] != (mods.Ref{ID: "10:1", Version: "1.0"}) {
		t.Fatalf("primary: %+v %v", got, err)
	}
	got, err = p.Resolve(ctx, "valheim", mods.Ref{ID: "10:3"})
	if err != nil || got[0] != (mods.Ref{ID: "10:3", Version: "9.0"}) {
		t.Fatalf("explicit file: %+v %v", got, err)
	}
	if _, err := p.Resolve(ctx, "valheim", mods.Ref{ID: "10:99"}); err == nil {
		t.Fatal("unknown file must fail")
	}
	for _, id := range []string{"abc", "10:x", "", "0", "-1:2"} {
		if _, err := p.Resolve(ctx, "valheim", mods.Ref{ID: id}); err == nil {
			t.Errorf("ref %q accepted", id)
		}
	}
	f.files = `{"files":[{"file_id":3,"category_name":"OPTIONAL"}]}`
	if _, err := p.Resolve(ctx, "valheim", mods.Ref{ID: "10"}); err == nil {
		t.Fatal("no main file must fail")
	}
}

func TestDownloadPremium(t *testing.T) {
	f := newFake(t)
	var buf bytes.Buffer
	art, err := f.provider(testKey).Download(context.Background(), "valheim", mods.Ref{ID: "10:2"}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if art.Filename != "new.zip" || art.Size != int64(len(f.payload)) || art.SHA256 != "" || !bytes.Equal(buf.Bytes(), f.payload) {
		t.Fatalf("artifact %+v", art)
	}
	if f.cdnKey != "" {
		t.Fatal("API key was sent to the CDN")
	}
}

func TestDownloadNonPremium(t *testing.T) {
	f := newFake(t)
	f.premium = false
	_, err := f.provider(testKey).Download(context.Background(), "valheim", mods.Ref{ID: "10:2"}, io.Discard)
	if !errors.Is(err, mods.ErrManualDownload) {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(err.Error(), testKey) {
		t.Fatal("error contains key")
	}
}

func TestDownloadHostAllowlist(t *testing.T) {
	for u, ok := range map[string]bool{
		"https://cf-files.nexus-cdn.com/x.zip":    true,
		"https://supporter-files.nexus-cdn.com/x": true,
		"https://www.nexusmods.com/x":             true,
		"http://cf-files.nexus-cdn.com/x":         false,
		"https://nexus-cdn.com.evil.test/x":       false,
		"https://evilnexus-cdn.com/x":             false,
		"https://evil.test/nexus-cdn.com":         false,
		"https://nexusmods.com/x":                 false,
	} {
		pu, _ := url.Parse(u)
		if allowedCDN(pu) != ok {
			t.Errorf("allowedCDN(%s) = %v", u, !ok)
		}
	}
	// Default provider refuses a link pointing at an arbitrary host.
	f := newFake(t)
	f.cdn = f.srv.URL + "/cdn/file" // http://127.0.0.1
	p := f.provider(testKey)
	p.allowDownloadURL = allowedCDN
	var buf bytes.Buffer
	_, err := p.Download(context.Background(), "valheim", mods.Ref{ID: "10:2"}, &buf)
	if err == nil || buf.Len() != 0 {
		t.Fatalf("disallowed host fetched: %v", err)
	}
}

func TestDownloadSizeCap(t *testing.T) {
	f := newFake(t)
	f.payload = bytes.Repeat([]byte("x"), 100)
	p := f.provider(testKey)
	p.maxBytes = 50
	var buf bytes.Buffer
	if _, err := p.Download(context.Background(), "valheim", mods.Ref{ID: "10:2"}, &buf); err == nil || !strings.Contains(err.Error(), "1 GiB") {
		t.Fatalf("got %v", err)
	}
	p.maxBytes = 100
	if _, err := p.Download(context.Background(), "valheim", mods.Ref{ID: "10:2"}, io.Discard); err != nil {
		t.Fatalf("exact size: %v", err)
	}
}

func TestRateLimited(t *testing.T) {
	f := newFake(t)
	f.status["files"] = 429
	p := f.provider(testKey)
	_, err := p.Resolve(context.Background(), "valheim", mods.Ref{ID: "10"})
	if !errors.Is(err, ErrRateLimited) || !strings.Contains(err.Error(), "rate limit") {
		t.Fatalf("got %v", err)
	}
	f.status["files"] = 404
	if _, err := p.Resolve(context.Background(), "valheim", mods.Ref{ID: "10"}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("404: %v", err)
	}
}

func TestName(t *testing.T) {
	if NewProvider(nil, nil).Name() != "nexus" {
		t.Fatal("name")
	}
}
