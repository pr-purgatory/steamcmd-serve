// Package thunderstore implements the mods.Provider for Thunderstore communities.
package thunderstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"steamcmd-serve/internal/mods"
)

const (
	defaultBaseURL = "https://thunderstore.io"
	userAgent      = "steamcmd-serve (+https://github.com/OWNER/steamcmd-serve)"
	maxDownload    = 1 << 30
	maxResults     = 50
)

// Provider talks to the public Thunderstore API.
type Provider struct {
	cacheDir string
	client   *http.Client
	baseURL  string
	maxBytes int64
	now      func() time.Time
	// allowURL vets the initial download URL; tests replace it.
	allowURL func(*url.URL) bool

	mu    sync.Mutex
	comms map[string]*community
}

var _ mods.Provider = (*Provider)(nil)

// New returns a provider caching indexes under cacheDir. A nil client gets
// sensible connection timeouts (no overall timeout, so large downloads work).
func New(cacheDir string, client *http.Client) *Provider {
	if client == nil {
		client = &http.Client{Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout:   15 * time.Second,
			ResponseHeaderTimeout: 60 * time.Second,
			IdleConnTimeout:       90 * time.Second,
		}}
	}
	return &Provider{
		cacheDir: cacheDir,
		client:   client,
		baseURL:  defaultBaseURL,
		maxBytes: maxDownload,
		now:      time.Now,
		allowURL: allowedDownloadURL,
		comms:    map[string]*community{},
	}
}

func (*Provider) Name() string { return "thunderstore" }

func allowedDownloadURL(u *url.URL) bool {
	if u.Scheme != "https" {
		return false
	}
	h := strings.ToLower(u.Hostname())
	return h == "thunderstore.io" || strings.HasSuffix(h, ".thunderstore.io")
}

func refsOf(deps []string) []mods.Ref {
	var out []mods.Ref
	for _, d := range deps {
		if id, v, err := splitDep(d); err == nil {
			out = append(out, mods.Ref{ID: id, Version: v})
		}
	}
	return out
}

// Search lists matching, non-deprecated packages (at most 50).
func (p *Provider) Search(ctx context.Context, scope, query string) ([]mods.Package, error) {
	ix, err := p.load(ctx, scope)
	if err != nil {
		return nil, err
	}
	q := strings.ToLower(strings.TrimSpace(query))
	wantPacks := strings.Contains(q, "bepinex")

	type hit struct {
		p    *pkg
		rank int
	}
	var hits []hit
	for _, pk := range ix.pkgs {
		if pk.Deprecated || len(pk.Versions) == 0 {
			continue
		}
		if mods.IsBepInExPack(pk.FullName) && !wantPacks {
			continue
		}
		rank := 0
		if q != "" {
			rank = matchRank(pk, q)
			if rank < 0 {
				continue
			}
		}
		hits = append(hits, hit{pk, rank})
		if q == "" && len(hits) == maxResults {
			break
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].rank < hits[j].rank })
	if len(hits) > maxResults {
		hits = hits[:maxResults]
	}
	out := make([]mods.Package, 0, len(hits))
	for _, h := range hits {
		latest := h.p.Versions[0]
		out = append(out, mods.Package{
			Ref:          mods.Ref{ID: h.p.FullName, Version: latest.Number},
			Name:         h.p.Name,
			Author:       h.p.Owner,
			Summary:      latest.Description,
			Dependencies: refsOf(latest.Dependencies),
			Categories:   h.p.Categories,
		})
	}
	return out, nil
}

// matchRank: 0 exact name, 1 name prefix, 2 name substring, 3 full name,
// 4 owner, 5 description; -1 no match.
func matchRank(pk *pkg, q string) int {
	name := strings.ToLower(pk.Name)
	switch {
	case name == q || strings.ToLower(pk.FullName) == q:
		return 0
	case strings.HasPrefix(name, q):
		return 1
	case strings.Contains(name, q):
		return 2
	case strings.Contains(strings.ToLower(pk.FullName), q):
		return 3
	case strings.Contains(strings.ToLower(pk.Owner), q):
		return 4
	case strings.Contains(strings.ToLower(pk.Versions[0].Description), q):
		return 5
	}
	return -1
}

func (ix *index) lookup(id, ver string) (*pkg, *version, error) {
	pk := ix.find(id)
	if pk == nil {
		return nil, nil, fmt.Errorf("thunderstore: unknown package %q", id)
	}
	if ver == "" {
		if len(pk.Versions) == 0 {
			return nil, nil, fmt.Errorf("thunderstore: package %q has no versions", id)
		}
		return pk, &pk.Versions[0], nil
	}
	v := pk.version(ver)
	if v == nil {
		return nil, nil, fmt.Errorf("thunderstore: unknown version %q of package %q", ver, id)
	}
	return pk, v, nil
}

// Resolve returns ref's dependency closure, dependencies first, ref last.
// A package required at several versions resolves to the highest.
func (p *Provider) Resolve(ctx context.Context, scope string, ref mods.Ref) ([]mods.Ref, error) {
	ix, err := p.load(ctx, scope)
	if err != nil {
		return nil, err
	}
	_, rv, err := ix.lookup(ref.ID, ref.Version)
	if err != nil {
		return nil, err
	}
	chosen := map[string]string{ref.ID: rv.Number}

	// Fixpoint: raise chosen versions until every requirement is satisfied.
	for changed := true; changed; {
		changed = false
		seen := map[string]bool{}
		var walk func(id string) error
		walk = func(id string) error {
			if seen[id] {
				return nil
			}
			seen[id] = true
			_, v, err := ix.lookup(id, chosen[id])
			if err != nil {
				return err
			}
			for _, d := range v.Dependencies {
				did, dver, err := splitDep(d)
				if err != nil {
					return fmt.Errorf("thunderstore: %s: %w", v.FullName, err)
				}
				if _, _, err := ix.lookup(did, dver); err != nil {
					return fmt.Errorf("%w (required by %s)", err, v.FullName)
				}
				if cur, ok := chosen[did]; !ok || semverLess(cur, dver) {
					chosen[did] = dver
					changed = true
				}
				if err := walk(did); err != nil {
					return err
				}
			}
			return nil
		}
		if err := walk(ref.ID); err != nil {
			return nil, err
		}
	}

	var out []mods.Ref
	state := map[string]int{} // 1 visiting, 2 done
	var path []string
	var visit func(id string) error
	visit = func(id string) error {
		switch state[id] {
		case 2:
			return nil
		case 1:
			return fmt.Errorf("thunderstore: dependency cycle: %s -> %s", strings.Join(path, " -> "), id)
		}
		state[id] = 1
		path = append(path, id)
		_, v, err := ix.lookup(id, chosen[id])
		if err != nil {
			return err
		}
		for _, d := range v.Dependencies {
			did, _, _ := splitDep(d)
			if err := visit(did); err != nil {
				return err
			}
		}
		path = path[:len(path)-1]
		state[id] = 2
		out = append(out, mods.Ref{ID: id, Version: chosen[id]})
		return nil
	}
	if err := visit(ref.ID); err != nil {
		return nil, err
	}
	return out, nil
}

func semverLess(a, b string) bool {
	x, err1 := parseSemver(a)
	y, err2 := parseSemver(b)
	if err1 != nil || err2 != nil {
		return false
	}
	return x.cmp(y) < 0
}

// Download streams the package zip to w.
func (p *Provider) Download(ctx context.Context, scope string, ref mods.Ref, w io.Writer) (mods.Artifact, error) {
	ix, err := p.load(ctx, scope)
	if err != nil {
		return mods.Artifact{}, err
	}
	_, v, err := ix.lookup(ref.ID, ref.Version)
	if err != nil {
		return mods.Artifact{}, err
	}
	u, err := url.Parse(v.DownloadURL)
	if err != nil || !p.allowURL(u) {
		return mods.Artifact{}, fmt.Errorf("thunderstore: refusing download URL %q for %s", v.DownloadURL, v.FullName)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return mods.Artifact{}, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := p.client.Do(req)
	if err != nil {
		return mods.Artifact{}, fmt.Errorf("thunderstore: download %s: %w", v.FullName, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return mods.Artifact{}, fmt.Errorf("thunderstore: download %s: HTTP %d", v.FullName, resp.StatusCode)
	}
	if resp.ContentLength > p.maxBytes {
		return mods.Artifact{}, fmt.Errorf("thunderstore: download %s exceeds %d byte limit", v.FullName, p.maxBytes)
	}
	n, err := io.Copy(w, io.LimitReader(resp.Body, p.maxBytes+1))
	if err != nil {
		return mods.Artifact{}, fmt.Errorf("thunderstore: download %s: %w", v.FullName, err)
	}
	if n > p.maxBytes {
		return mods.Artifact{}, errors.New("thunderstore: download " + v.FullName + " exceeds size limit")
	}
	return mods.Artifact{Filename: v.FullName + ".zip", Size: n}, nil
}
