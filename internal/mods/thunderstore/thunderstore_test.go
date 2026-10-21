package thunderstore

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"steamcmd-serve/internal/mods"
)

type fake struct {
	srv      *httptest.Server
	indexReq atomic.Int32
	dlReq    atomic.Int32
	failIdx  atomic.Bool
	condHits atomic.Int32
	body     []byte
	dl       []byte
	dlStatus int
}

func newFake(t *testing.T) *fake {
	t.Helper()
	raw, err := os.ReadFile("testdata/index.json")
	if err != nil {
		t.Fatal(err)
	}
	f := &fake{dl: []byte("PK-fake-zip"), dlStatus: 200}
	mux := http.NewServeMux()
	mux.HandleFunc("/c/valheim/api/v1/package/", func(w http.ResponseWriter, r *http.Request) {
		f.indexReq.Add(1)
		time.Sleep(30 * time.Millisecond) // widen the race window
		if f.failIdx.Load() {
			http.Error(w, "boom", 500)
			return
		}
		if r.Header.Get("If-None-Match") == `"v1"` {
			f.condHits.Add(1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Content-Type", "application/json")
		w.Write(f.body)
	})
	mux.HandleFunc("/package/download/", func(w http.ResponseWriter, r *http.Request) {
		f.dlReq.Add(1)
		if r.Header.Get("User-Agent") != userAgent {
			http.Error(w, "bad ua", 400)
			return
		}
		if f.dlStatus != 200 {
			http.Error(w, "nope", f.dlStatus)
			return
		}
		w.Write(f.dl)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	f.body = bytes.ReplaceAll(raw, []byte("__BASE__"), []byte(f.srv.URL))
	return f
}

func (f *fake) provider(t *testing.T) *Provider {
	p := New(t.TempDir(), f.srv.Client())
	p.baseURL = f.srv.URL
	host := mustParse(f.srv.URL).Host
	p.allowURL = func(u *url.URL) bool { return u.Host == host }
	return p
}

func mustParse(s string) *url.URL { u, _ := url.Parse(s); return u }

func ids(ps []mods.Package) []string {
	var o []string
	for _, p := range ps {
		o = append(o, p.Ref.ID)
	}
	return o
}

func TestName(t *testing.T) {
	if New(t.TempDir(), nil).Name() != "thunderstore" {
		t.Fatal("name")
	}
}

func TestSearch(t *testing.T) {
	f := newFake(t)
	p := f.provider(t)
	ctx := context.Background()

	got, err := p.Search(ctx, "valheim", "spawn")
	if err != nil {
		t.Fatal(err)
	}
	// Spawner (prefix of name) before Spawn? "Spawn" is exact for Top-Spawn.
	want := []string{"Top-Spawn", "Author-Spawner"}
	if fmt.Sprint(ids(got)) != fmt.Sprint(want) {
		t.Fatalf("got %v want %v (deprecated Old-Spawner2 must be skipped)", ids(got), want)
	}
	if got[1].Author != "Author" || got[1].Ref.Version != "1.0.0" || got[1].Summary == "" ||
		len(got[1].Dependencies) != 1 || got[1].Dependencies[0] != (mods.Ref{ID: "denikson-BepInExPack_Valheim", Version: "5.4.2202"}) ||
		len(got[1].Categories) != 1 {
		t.Fatalf("bad package %+v", got[1])
	}

	// substring name ranks below prefix; owner/description matches last; case-insensitive
	got, _ = p.Search(ctx, "valheim", "SHARED")
	if fmt.Sprint(ids(got)) != "[Shared-Shared]" || got[0].Ref.Version != "1.10.0" {
		t.Fatalf("shared: %+v", got)
	}
	got, _ = p.Search(ctx, "valheim", "console command")
	if fmt.Sprint(ids(got)) != "[Author-Spawner]" {
		t.Fatalf("description match: %v", ids(got))
	}

	// BepInExPack hidden unless named
	got, _ = p.Search(ctx, "valheim", "valheim")
	for _, id := range ids(got) {
		if mods.IsBepInExPack(id) {
			t.Fatalf("pack leaked: %v", ids(got))
		}
	}
	got, _ = p.Search(ctx, "valheim", "bepinex")
	if fmt.Sprint(ids(got)) != "[denikson-BepInExPack_Valheim]" {
		t.Fatalf("bepinex: %v", ids(got))
	}

	// empty query keeps index order, skips deprecated/packs
	got, _ = p.Search(ctx, "valheim", "")
	if len(got) == 0 || got[0].Ref.ID != "Author-Spawner" {
		t.Fatalf("empty: %v", ids(got))
	}
	for _, id := range ids(got) {
		if id == "Old-Spawner2" || mods.IsBepInExPack(id) {
			t.Fatalf("empty query returned %s", id)
		}
	}
}

func TestSearchLimit(t *testing.T) {
	f := newFake(t)
	var sb strings.Builder
	sb.WriteString("[")
	for i := 0; i < 80; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `{"name":"M%d","full_name":"o-M%d","owner":"o","versions":[{"full_name":"o-M%d-1.0.0","version_number":"1.0.0","description":"d","download_url":"","dependencies":[]}]}`, i, i, i)
	}
	sb.WriteString("]")
	f.body = []byte(sb.String())
	p := f.provider(t)
	for _, q := range []string{"", "m"} {
		got, err := p.Search(context.Background(), "valheim", q)
		if err != nil || len(got) != 50 {
			t.Fatalf("q=%q len=%d err=%v", q, len(got), err)
		}
	}
}

func refStrs(rs []mods.Ref) string {
	var s []string
	for _, r := range rs {
		s = append(s, r.ID+"@"+r.Version)
	}
	return strings.Join(s, ",")
}

func TestResolve(t *testing.T) {
	f := newFake(t)
	p := f.provider(t)
	ctx := context.Background()

	got, err := p.Resolve(ctx, "valheim", mods.Ref{ID: "Author-Spawner", Version: "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	if refStrs(got) != "denikson-BepInExPack_Valheim@5.4.2202,Author-Spawner@1.0.0" {
		t.Fatalf("simple: %s", refStrs(got))
	}

	// empty version -> latest; shared dep resolves to highest (1.10.0 > 1.2.0 numerically),
	// whose own dep pulls the newer BepInExPack.
	got, err = p.Resolve(ctx, "valheim", mods.Ref{ID: "Top-Spawn"})
	if err != nil {
		t.Fatal(err)
	}
	want := "denikson-BepInExPack_Valheim@5.4.2351,Shared-Shared@1.10.0,Left-Left@1.0.0,Right-Right@1.0.0,Top-Spawn@1.0.0"
	if refStrs(got) != want {
		t.Fatalf("got  %s\nwant %s", refStrs(got), want)
	}

	if _, err := p.Resolve(ctx, "valheim", mods.Ref{ID: "Cyc-Alpha"}); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("cycle err: %v", err)
	}
	if _, err := p.Resolve(ctx, "valheim", mods.Ref{ID: "Broken-Mod"}); err == nil || !strings.Contains(err.Error(), "Ghost-Missing") {
		t.Fatalf("unknown dep err: %v", err)
	}
	if _, err := p.Resolve(ctx, "valheim", mods.Ref{ID: "Nope-Nope"}); err == nil || !strings.Contains(err.Error(), "Nope-Nope") {
		t.Fatalf("unknown pkg: %v", err)
	}
	if _, err := p.Resolve(ctx, "valheim", mods.Ref{ID: "Top-Spawn", Version: "9.9.9"}); err == nil || !strings.Contains(err.Error(), "9.9.9") {
		t.Fatalf("unknown version: %v", err)
	}
}

func TestSplitDep(t *testing.T) {
	if id, v, err := splitDep("A-B_c-1.2.3"); err != nil || id != "A-B_c" || v != "1.2.3" {
		t.Fatal(id, v, err)
	}
	for _, bad := range []string{"", "A-B", "A-B-1.2", "A-B-x.y.z", "A-B-C-1.0.0", "-B-1.0.0", "A--1.0.0"} {
		if _, _, err := splitDep(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestDownload(t *testing.T) {
	f := newFake(t)
	p := f.provider(t)
	ctx := context.Background()

	var buf bytes.Buffer
	art, err := p.Download(ctx, "valheim", mods.Ref{ID: "Author-Spawner", Version: "1.0.0"}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if art.Filename != "Author-Spawner-1.0.0.zip" || art.Size != int64(len(f.dl)) || art.SHA256 != "" || buf.String() != string(f.dl) {
		t.Fatalf("%+v %q", art, buf.String())
	}

	// size cap
	p.maxBytes = 5
	buf.Reset()
	if _, err := p.Download(ctx, "valheim", mods.Ref{ID: "Author-Spawner", Version: "1.0.0"}, &buf); err == nil {
		t.Fatal("cap not enforced")
	}

	// non-200
	p.maxBytes = maxDownload
	f.dlStatus = 404
	if _, err := p.Download(ctx, "valheim", mods.Ref{ID: "Author-Spawner", Version: "1.0.0"}, &buf); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("status err: %v", err)
	}
	// unknown package
	if _, err := p.Download(ctx, "valheim", mods.Ref{ID: "No-Pkg"}, &buf); err == nil {
		t.Fatal("expected error")
	}
}

func TestDownloadHostAllowlist(t *testing.T) {
	f := newFake(t)
	f.body = []byte(`[{"name":"Evil","full_name":"x-Evil","owner":"x","versions":[
	 {"full_name":"x-Evil-1.0.0","version_number":"1.0.0","download_url":"https://evil.example.com/a.zip"},
	 {"full_name":"x-Evil-1.0.1","version_number":"1.0.1","download_url":"http://thunderstore.io/a.zip"},
	 {"full_name":"x-Evil-1.0.2","version_number":"1.0.2","download_url":"https://thunderstore.io.evil.com/a.zip"}]}]`)
	p := New(t.TempDir(), f.srv.Client()) // default (strict) allowlist
	p.baseURL = f.srv.URL
	for _, v := range []string{"1.0.0", "1.0.1", "1.0.2"} {
		_, err := p.Download(context.Background(), "valheim", mods.Ref{ID: "x-Evil", Version: v}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "refusing") {
			t.Fatalf("%s: %v", v, err)
		}
	}
	if f.dlReq.Load() != 0 {
		t.Fatal("download request made")
	}
	for u, want := range map[string]bool{
		"https://thunderstore.io/package/download/a/b/1.0.0/": true,
		"https://gcdn.thunderstore.io/x.zip":                  true,
		"https://notthunderstore.io/x":                        false,
		"ftp://thunderstore.io/x":                             false,
	} {
		if got := allowedDownloadURL(mustParse(u)); got != want {
			t.Errorf("%s: %v", u, got)
		}
	}
}

func TestCacheFreshNoRequest(t *testing.T) {
	f := newFake(t)
	p := f.provider(t)
	ctx := context.Background()
	if _, err := p.Search(ctx, "valheim", "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Search(ctx, "valheim", "y"); err != nil {
		t.Fatal(err)
	}
	if f.indexReq.Load() != 1 {
		t.Fatalf("requests=%d", f.indexReq.Load())
	}
	// a new Provider over the same cache dir reads disk, still no request
	p2 := New(p.cacheDir, f.srv.Client())
	p2.baseURL = f.srv.URL
	if got, err := p2.Search(ctx, "valheim", "spawn"); err != nil || len(got) == 0 {
		t.Fatal(got, err)
	}
	if f.indexReq.Load() != 1 {
		t.Fatalf("requests after reopen=%d", f.indexReq.Load())
	}
	if _, err := os.Stat(p.cachePath("valheim")); err != nil {
		t.Fatal(err)
	}
}

func TestStaleNotModified(t *testing.T) {
	f := newFake(t)
	p := f.provider(t)
	ctx := context.Background()
	if _, err := p.Search(ctx, "valheim", "x"); err != nil {
		t.Fatal(err)
	}
	clock := time.Now().Add(25 * time.Hour)
	p.now = func() time.Time { return clock }
	got, err := p.Search(ctx, "valheim", "spawn")
	if err != nil || len(got) == 0 {
		t.Fatal(got, err)
	}
	if f.indexReq.Load() != 2 || f.condHits.Load() != 1 {
		t.Fatalf("req=%d cond=%d", f.indexReq.Load(), f.condHits.Load())
	}
	// refreshed timestamp: no further request
	if _, err := p.Search(ctx, "valheim", "spawn"); err != nil {
		t.Fatal(err)
	}
	if f.indexReq.Load() != 2 {
		t.Fatalf("req=%d", f.indexReq.Load())
	}
}

func TestStaleFallbackOnFailure(t *testing.T) {
	f := newFake(t)
	p := f.provider(t)
	ctx := context.Background()
	if _, err := p.Search(ctx, "valheim", "x"); err != nil {
		t.Fatal(err)
	}
	f.failIdx.Store(true)
	clock := time.Now().Add(48 * time.Hour)
	p.now = func() time.Time { return clock }
	got, err := p.Search(ctx, "valheim", "spawn")
	if err != nil || len(got) == 0 {
		t.Fatalf("expected stale data: %v %v", got, err)
	}
	if f.indexReq.Load() != 2 {
		t.Fatalf("req=%d", f.indexReq.Load())
	}
	// fresh provider with only the disk cache also falls back
	p2 := New(p.cacheDir, f.srv.Client())
	p2.baseURL = f.srv.URL
	p2.now = p.now
	if got, err := p2.Search(ctx, "valheim", "spawn"); err != nil || len(got) == 0 {
		t.Fatalf("disk fallback: %v %v", got, err)
	}
}

func TestNoCacheFailureIsError(t *testing.T) {
	f := newFake(t)
	f.failIdx.Store(true)
	p := f.provider(t)
	if _, err := p.Search(context.Background(), "valheim", "x"); err == nil {
		t.Fatal("expected error")
	}
	if _, err := p.Search(context.Background(), "../etc", "x"); err == nil {
		t.Fatal("expected invalid community error")
	}
}

func TestConcurrentSingleFetch(t *testing.T) {
	f := newFake(t)
	p := f.provider(t)
	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := p.Search(context.Background(), "valheim", "spawn"); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if n := f.indexReq.Load(); n != 1 {
		t.Fatalf("index fetches=%d, want 1", n)
	}
}

func TestSemverCompare(t *testing.T) {
	a, _ := parseSemver("1.10.0")
	b, _ := parseSemver("1.2.0")
	if a.cmp(b) <= 0 || b.cmp(a) >= 0 || a.cmp(a) != 0 {
		t.Fatal("numeric compare")
	}
	if _, err := parseSemver("1.2"); err == nil {
		t.Fatal("accepted 1.2")
	}
}
