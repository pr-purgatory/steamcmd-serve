// Package nexus implements the NexusMods mod provider and the storage of its API key.
//
// The API key is a secret. It is only ever sent in the "apikey" header to the Nexus API
// hosts, is stored sealed (internal/secrets), and never appears in errors or logs.
package nexus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Default endpoints.
const (
	DefaultRESTBase   = "https://api.nexusmods.com/v1"
	DefaultGraphQLURL = "https://api.nexusmods.com/v2/graphql"
)

// Errors returned by this package.
var (
	// ErrNoKey means no API key is configured.
	ErrNoKey = errors.New("add a NexusMods API key in Settings")
	// ErrInvalidKey means NexusMods rejected the API key.
	ErrInvalidKey = errors.New("NexusMods rejected this API key")
	// ErrRateLimited means the NexusMods rate limit is exhausted.
	ErrRateLimited = errors.New("NexusMods rate limit reached, try again later")
)

// Client talks to the NexusMods REST v1 and GraphQL v2 APIs.
type Client struct {
	HTTP       *http.Client
	RESTBase   string // default DefaultRESTBase, no trailing slash
	GraphQLURL string // default DefaultGraphQLURL
	Version    string // sent as Application-Version
}

func (c *Client) httpc() *http.Client {
	if c != nil && c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c *Client) restBase() string {
	if c.RESTBase != "" {
		return strings.TrimRight(c.RESTBase, "/")
	}
	return DefaultRESTBase
}

func (c *Client) gqlURL() string {
	if c.GraphQLURL != "" {
		return c.GraphQLURL
	}
	return DefaultGraphQLURL
}

func (c *Client) version() string {
	if c.Version != "" {
		return c.Version
	}
	return "dev"
}

// statusError is a non-2xx response. It carries no URL or headers.
type statusError struct{ Code int }

func (e *statusError) Error() string { return fmt.Sprintf("NexusMods returned HTTP %d", e.Code) }

// do sends an authenticated request and returns the response (caller closes the body).
// Statuses other than 2xx are converted: 401 -> ErrInvalidKey, 429 -> ErrRateLimited,
// anything else -> *statusError.
func (c *Client) do(ctx context.Context, key, method, rawURL string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, errors.New("nexus: bad request")
	}
	req.Header.Set("apikey", key)
	req.Header.Set("Application-Name", "steamcmd-serve")
	req.Header.Set("Application-Version", c.version())
	req.Header.Set("User-Agent", "steamcmd-serve/"+c.version())
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpc().Do(req)
	if err != nil {
		return nil, scrub(err)
	}
	switch {
	case resp.StatusCode/100 == 2:
		return resp, nil
	case resp.StatusCode == http.StatusUnauthorized:
		resp.Body.Close()
		return nil, ErrInvalidKey
	case resp.StatusCode == http.StatusTooManyRequests:
		resp.Body.Close()
		return nil, ErrRateLimited
	}
	resp.Body.Close()
	return nil, &statusError{resp.StatusCode}
}

// scrub drops the URL from transport errors (download URLs carry signed tokens).
func scrub(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		if errors.Is(ue.Err, context.Canceled) || errors.Is(ue.Err, context.DeadlineExceeded) {
			return ue.Err
		}
		return fmt.Errorf("NexusMods request failed: %w", ue.Err)
	}
	return err
}

func (c *Client) getJSON(ctx context.Context, key, path string, out any) error {
	resp, err := c.do(ctx, key, http.MethodGet, c.restBase()+path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(out); err != nil {
		return errors.New("NexusMods returned an unreadable response")
	}
	return nil
}

// Account is what the Nexus API says about the key's owner.
type Account struct {
	HasValue bool   `json:"hasValue"`
	Premium  bool   `json:"premium,omitempty"`
	Username string `json:"username,omitempty"`
}

// Validate checks key against /users/validate.json (ErrInvalidKey for a bad key).
func (c *Client) Validate(ctx context.Context, key string) (Account, error) {
	var v struct {
		Name    string `json:"name"`
		Premium bool   `json:"is_premium"`
	}
	if err := c.getJSON(ctx, key, "/users/validate.json", &v); err != nil {
		return Account{}, err
	}
	return Account{HasValue: true, Premium: v.Premium, Username: v.Name}, nil
}

// File is one entry of a mod's file list.
type File struct {
	FileID    int64  `json:"file_id"`
	Name      string `json:"name"`
	Version   string `json:"version"`
	Category  string `json:"category_name"`
	IsPrimary bool   `json:"is_primary"`
	SizeKB    int64  `json:"size_kb"`
	FileName  string `json:"file_name"`
	Uploaded  int64  `json:"uploaded_timestamp"`
}

// Files lists a mod's files.
func (c *Client) Files(ctx context.Context, key, domain string, modID int64) ([]File, error) {
	var v struct {
		Files []File `json:"files"`
	}
	path := "/games/" + url.PathEscape(domain) + "/mods/" + strconv.FormatInt(modID, 10) + "/files.json"
	if err := c.getJSON(ctx, key, path, &v); err != nil {
		var se *statusError
		if errors.As(err, &se) && se.Code == http.StatusNotFound {
			return nil, fmt.Errorf("NexusMods mod %d not found for game %q", modID, domain)
		}
		return nil, err
	}
	return v.Files, nil
}
