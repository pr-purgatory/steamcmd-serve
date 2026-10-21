package nexus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

	"steamcmd-serve/internal/mods"
)

// maxDownload caps a single mod archive.
const maxDownload = 1 << 30

// searchQuery lists mods of one game whose name matches a term.
//
// Verified against the live schema (https://api.nexusmods.com/v2/graphql, introspection
// and a real query, anonymous): `mods(filter:, count:)` with `gameDomainName` and `name`
// filters of the form [{value, op}]. WILDCARD matches the term anywhere in the name;
// explicit "*" characters in the term match nothing, so the term is sent as typed.
const searchQuery = `query ($domain: String!, $q: String!) {
  mods(filter: {gameDomainName: [{value: $domain}], name: [{value: $q, op: WILDCARD}]}, count: 30) {
    nodes { modId name summary author version }
  }
}`

// browseQuery is searchQuery without the name filter, used for an empty search.
const browseQuery = `query ($domain: String!) {
  mods(filter: {gameDomainName: [{value: $domain}]}, count: 30) {
    nodes { modId name summary author version }
  }
}`

// Provider is the NexusMods mods.Provider.
type Provider struct {
	client *Client
	key    func(context.Context) (string, error)

	// allowDownloadURL decides which CDN URLs may be fetched; tests replace it.
	allowDownloadURL func(*url.URL) bool
	maxBytes         int64
}

var _ mods.Provider = (*Provider)(nil)

// NewProvider returns a Provider. key supplies the API key per call (ErrNoKey when
// unset), typically Keyring.Key. client may be nil (production defaults).
func NewProvider(key func(context.Context) (string, error), client *Client) *Provider {
	if client == nil {
		client = &Client{}
	}
	return &Provider{client: client, key: key, allowDownloadURL: allowedCDN, maxBytes: maxDownload}
}

// Name implements mods.Provider.
func (p *Provider) Name() string { return "nexus" }

func allowedCDN(u *url.URL) bool {
	h := strings.ToLower(u.Hostname())
	return u.Scheme == "https" && (strings.HasSuffix(h, ".nexus-cdn.com") || strings.HasSuffix(h, ".nexusmods.com"))
}

func (p *Provider) apiKey(ctx context.Context) (string, error) {
	if p.key == nil {
		return "", ErrNoKey
	}
	key, err := p.key(ctx)
	if err != nil {
		return "", err
	}
	if key == "" {
		return "", ErrNoKey
	}
	return key, nil
}

// Search lists MODS (not files) of the Nexus game domain matching q. Installing one
// needs a specific file: Resolve turns the mod id into "<modId>:<fileId>".
func (p *Provider) Search(ctx context.Context, domain, q string) ([]mods.Package, error) {
	key, err := p.apiKey(ctx)
	if err != nil {
		return nil, err
	}
	query, vars := searchQuery, map[string]any{"domain": domain, "q": q}
	if strings.TrimSpace(q) == "" {
		query, vars = browseQuery, map[string]any{"domain": domain}
	}
	body, _ := json.Marshal(map[string]any{"query": query, "variables": vars})
	resp, err := p.client.do(ctx, key, http.MethodPost, p.client.gqlURL(), strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Data struct {
			Mods struct {
				Nodes []struct {
					ModID   int64  `json:"modId"`
					Name    string `json:"name"`
					Summary string `json:"summary"`
					Author  string `json:"author"`
					Version string `json:"version"`
				} `json:"nodes"`
			} `json:"mods"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&out); err != nil {
		return nil, errors.New("NexusMods returned an unreadable response")
	}
	if len(out.Errors) > 0 {
		return nil, errors.New("NexusMods search failed: " + out.Errors[0].Message)
	}
	pkgs := make([]mods.Package, 0, len(out.Data.Mods.Nodes))
	for _, n := range out.Data.Mods.Nodes {
		pkgs = append(pkgs, mods.Package{
			Ref:          mods.Ref{ID: strconv.FormatInt(n.ModID, 10), Version: n.Version},
			Name:         n.Name,
			Author:       n.Author,
			Summary:      n.Summary,
			Dependencies: []mods.Ref{},
		})
	}
	return pkgs, nil
}

// parseRef splits "<modId>" or "<modId>:<fileId>" (fileId 0 = unspecified).
func parseRef(id string) (modID, fileID int64, err error) {
	m, f, hasFile := strings.Cut(id, ":")
	modID, err = strconv.ParseInt(m, 10, 64)
	if err != nil || modID <= 0 {
		return 0, 0, fmt.Errorf("invalid NexusMods mod id %q (want <modId> or <modId>:<fileId>)", id)
	}
	if hasFile {
		fileID, err = strconv.ParseInt(f, 10, 64)
		if err != nil || fileID <= 0 {
			return 0, 0, fmt.Errorf("invalid NexusMods file id in %q", id)
		}
	}
	return modID, fileID, nil
}

// Resolve picks the file to install and returns exactly one Ref "<modId>:<fileId>".
// Nexus has no machine-readable dependency data (only free-text requirements on the
// website), so the result never contains dependencies; users install those themselves.
// Without a file id the primary file is used, else the newest MAIN-category file.
func (p *Provider) Resolve(ctx context.Context, domain string, ref mods.Ref) ([]mods.Ref, error) {
	key, err := p.apiKey(ctx)
	if err != nil {
		return nil, err
	}
	modID, fileID, err := parseRef(ref.ID)
	if err != nil {
		return nil, err
	}
	files, err := p.client.Files(ctx, key, domain, modID)
	if err != nil {
		return nil, err
	}
	var pick *File
	for i := range files {
		f := &files[i]
		switch {
		case fileID != 0:
			if f.FileID == fileID {
				pick = f
			}
		case f.IsPrimary:
			if pick == nil || !pick.IsPrimary || f.Uploaded > pick.Uploaded {
				pick = f
			}
		case strings.EqualFold(f.Category, "MAIN") && (pick == nil || (!pick.IsPrimary && f.Uploaded > pick.Uploaded)):
			pick = f
		}
	}
	if pick == nil {
		if fileID != 0 {
			return nil, fmt.Errorf("NexusMods file %d not found in mod %d", fileID, modID)
		}
		return nil, fmt.Errorf("NexusMods mod %d has no main file; choose a file as <modId>:<fileId>", modID)
	}
	return []mods.Ref{{ID: fmt.Sprintf("%d:%d", modID, pick.FileID), Version: pick.Version}}, nil
}

// Download writes the archive for ref ("<modId>:<fileId>") to w. Without Premium Nexus
// only hands out download links via the website, so it returns mods.ErrManualDownload.
func (p *Provider) Download(ctx context.Context, domain string, ref mods.Ref, w io.Writer) (mods.Artifact, error) {
	key, err := p.apiKey(ctx)
	if err != nil {
		return mods.Artifact{}, err
	}
	modID, fileID, err := parseRef(ref.ID)
	if err != nil {
		return mods.Artifact{}, err
	}
	if fileID == 0 {
		return mods.Artifact{}, fmt.Errorf("NexusMods ref %q needs a file id (<modId>:<fileId>)", ref.ID)
	}
	manual := fmt.Errorf("%w: NexusMods only provides direct downloads to Premium members; download the file on nexusmods.com and upload it here", mods.ErrManualDownload)

	files, err := p.client.Files(ctx, key, domain, modID)
	if err != nil {
		return mods.Artifact{}, err
	}
	var file *File
	for i := range files {
		if files[i].FileID == fileID {
			file = &files[i]
		}
	}
	if file == nil {
		return mods.Artifact{}, fmt.Errorf("NexusMods file %d not found in mod %d", fileID, modID)
	}

	linkPath := fmt.Sprintf("/games/%s/mods/%d/files/%d/download_link.json", url.PathEscape(domain), modID, fileID)
	var links []struct {
		URI string `json:"URI"`
	}
	if err := p.client.getJSON(ctx, key, linkPath, &links); err != nil {
		var se *statusError
		if errors.As(err, &se) && se.Code == http.StatusForbidden {
			return mods.Artifact{}, manual
		}
		return mods.Artifact{}, err
	}
	if len(links) == 0 || links[0].URI == "" {
		return mods.Artifact{}, manual
	}
	u, err := url.Parse(links[0].URI)
	if err != nil || !p.allowDownloadURL(u) {
		return mods.Artifact{}, errors.New("NexusMods returned a download link on an unexpected host; refusing to fetch it")
	}

	// The CDN request carries no API key and may only redirect to allowed hosts.
	hc := *p.client.httpc()
	hc.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 || !p.allowDownloadURL(req.URL) {
			return errors.New("download redirected to an unexpected host")
		}
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return mods.Artifact{}, errors.New("nexus: bad download request")
	}
	req.Header.Set("User-Agent", "steamcmd-serve/"+p.client.version())
	resp, err := hc.Do(req)
	if err != nil {
		return mods.Artifact{}, scrub(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return mods.Artifact{}, ErrRateLimited
	}
	if resp.StatusCode == http.StatusForbidden {
		return mods.Artifact{}, manual
	}
	if resp.StatusCode != http.StatusOK {
		return mods.Artifact{}, &statusError{resp.StatusCode}
	}
	if resp.ContentLength > p.maxBytes {
		return mods.Artifact{}, errors.New("NexusMods file is larger than the 1 GiB limit")
	}
	n, err := io.Copy(w, io.LimitReader(resp.Body, p.maxBytes+1))
	if err != nil {
		return mods.Artifact{}, fmt.Errorf("download failed: %w", scrub(err))
	}
	if n > p.maxBytes {
		return mods.Artifact{}, errors.New("NexusMods file is larger than the 1 GiB limit")
	}
	name := path.Base(strings.ReplaceAll(file.FileName, "\\", "/"))
	if name == "." || name == "/" {
		name = fmt.Sprintf("%d-%d.zip", modID, fileID)
	}
	// Nexus publishes MD5 only, not SHA-256: leave SHA256 empty.
	return mods.Artifact{Filename: name, Size: n}, nil
}
