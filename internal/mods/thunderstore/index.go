package thunderstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

const indexTTL = 24 * time.Hour

// failBackoff stops a broken upstream from being hit on every call while a stale cache is served.
const failBackoff = time.Minute

var communityRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

type version struct {
	FullName     string   `json:"full_name"`
	Number       string   `json:"version_number"`
	Description  string   `json:"description"`
	DownloadURL  string   `json:"download_url"`
	Dependencies []string `json:"dependencies"`
	FileSize     int64    `json:"file_size"`
}

type pkg struct {
	Name       string    `json:"name"`
	FullName   string    `json:"full_name"`
	Owner      string    `json:"owner"`
	Deprecated bool      `json:"is_deprecated"`
	Categories []string  `json:"categories"`
	Versions   []version `json:"versions"` // newest first
}

// index is a parsed community package index.
type index struct {
	pkgs   []*pkg
	byName map[string]*pkg
	loaded time.Time // when the data was last confirmed fresh
}

func (ix *index) find(id string) *pkg { return ix.byName[id] }

func (p *pkg) version(v string) *version {
	for i := range p.Versions {
		if p.Versions[i].Number == v {
			return &p.Versions[i]
		}
	}
	return nil
}

type validators struct {
	ETag         string `json:"etag,omitempty"`
	LastModified string `json:"last_modified,omitempty"`
}

type community struct {
	mu       sync.Mutex
	idx      *index
	failedAt time.Time
}

func (p *Provider) comm(name string) *community {
	p.mu.Lock()
	defer p.mu.Unlock()
	c := p.comms[name]
	if c == nil {
		c = &community{}
		p.comms[name] = c
	}
	return c
}

func (p *Provider) cachePath(name string) string { return filepath.Join(p.cacheDir, name+".json") }
func (p *Provider) metaPath(name string) string  { return filepath.Join(p.cacheDir, name+".meta.json") }

// load returns a fresh-enough index for the community, fetching at most once
// across concurrent callers.
func (p *Provider) load(ctx context.Context, name string) (*index, error) {
	if !communityRE.MatchString(name) {
		return nil, fmt.Errorf("thunderstore: invalid community %q", name)
	}
	c := p.comm(name)
	c.mu.Lock()
	defer c.mu.Unlock()

	now := p.now()
	if c.idx != nil && now.Sub(c.idx.loaded) < indexTTL {
		return c.idx, nil
	}
	// Pick up a disk cache (first use, or written by an earlier process).
	var diskMod time.Time
	if st, err := os.Stat(p.cachePath(name)); err == nil {
		diskMod = st.ModTime()
		if c.idx == nil {
			if ix, err := readIndexFile(p.cachePath(name)); err == nil {
				ix.loaded = diskMod
				c.idx = ix
			} else {
				slog.Warn("thunderstore: ignoring unreadable index cache", "community", name, "err", err)
				diskMod = time.Time{}
			}
		}
		if c.idx != nil && now.Sub(c.idx.loaded) < indexTTL {
			return c.idx, nil
		}
	}
	if c.idx != nil && !c.failedAt.IsZero() && now.Sub(c.failedAt) < failBackoff {
		return c.idx, nil
	}

	ix, err := p.refresh(ctx, name, c.idx != nil && !diskMod.IsZero())
	if err != nil {
		if c.idx != nil {
			c.failedAt = now
			slog.Warn("thunderstore: index refresh failed, using stale cache", "community", name, "err", err)
			return c.idx, nil
		}
		return nil, err
	}
	c.failedAt = time.Time{}
	if ix == nil { // 304
		c.idx.loaded = now
		return c.idx, nil
	}
	c.idx = ix
	return ix, nil
}

// refresh fetches the index. It returns (nil, nil) on 304 Not Modified.
func (p *Provider) refresh(ctx context.Context, name string, conditional bool) (*index, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/c/"+name+"/api/v1/package/", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	if conditional {
		var v validators
		if b, err := os.ReadFile(p.metaPath(name)); err == nil && json.Unmarshal(b, &v) == nil {
			if v.ETag != "" {
				req.Header.Set("If-None-Match", v.ETag)
			}
			if v.LastModified != "" {
				req.Header.Set("If-Modified-Since", v.LastModified)
			}
		}
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("thunderstore: fetch index: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotModified:
		if conditional {
			now := p.now()
			_ = os.Chtimes(p.cachePath(name), now, now)
			return nil, nil
		}
		return nil, errors.New("thunderstore: unexpected 304 for unconditional request")
	case http.StatusOK:
	default:
		return nil, fmt.Errorf("thunderstore: fetch index: HTTP %d", resp.StatusCode)
	}

	if err := os.MkdirAll(p.cacheDir, 0o755); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(p.cacheDir, name+".*.tmp")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	// Decode while teeing to the cache file: the body is read exactly once.
	ix, err := decodeIndex(io.TeeReader(resp.Body, tmp))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, fmt.Errorf("thunderstore: decode index: %w", err)
	}
	now := p.now()
	_ = os.Chtimes(tmp.Name(), now, now)
	if err := os.Rename(tmp.Name(), p.cachePath(name)); err != nil {
		return nil, err
	}
	v := validators{ETag: resp.Header.Get("ETag"), LastModified: resp.Header.Get("Last-Modified")}
	if b, err := json.Marshal(v); err == nil {
		_ = os.WriteFile(p.metaPath(name), b, 0o644)
	}
	ix.loaded = now
	return ix, nil
}

func readIndexFile(path string) (*index, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return decodeIndex(f)
}

// decodeIndex stream-decodes a JSON array of packages one element at a time.
func decodeIndex(r io.Reader) (*index, error) {
	dec := json.NewDecoder(r)
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '[' {
		return nil, errors.New("index is not a JSON array")
	}
	ix := &index{byName: map[string]*pkg{}}
	for dec.More() {
		p := new(pkg)
		if err := dec.Decode(p); err != nil {
			return nil, err
		}
		if p.FullName == "" {
			continue
		}
		ix.pkgs = append(ix.pkgs, p)
		ix.byName[p.FullName] = p
	}
	if _, err := dec.Token(); err != nil { // closing ]
		return nil, err
	}
	return ix, nil
}
