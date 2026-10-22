package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	healthTimeout = 5 * time.Second
	configSubdir  = "steamcmd-serve-client"
	configFile    = "config.json"
)

// normalizeURL turns user input into a canonical controller base URL
// (scheme://host[:port], no trailing slash).
func normalizeURL(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errors.New("enter the controller address")
	}
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", errors.New("that does not look like a valid address")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("only http:// and https:// addresses are supported")
	}
	if u.User != nil {
		return "", errors.New("do not put credentials in the address; log in on the controller page instead")
	}
	if u.Hostname() == "" || u.Opaque != "" {
		return "", errors.New("the address has no host")
	}
	if u.Path != "" && u.Path != "/" {
		return "", errors.New("use the controller's base address without a path")
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", errors.New("the address must not contain a query or fragment")
	}
	return u.Scheme + "://" + u.Host, nil
}

// checkHealth GETs <base>/healthz and expects 204 No Content.
func checkHealth(base string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/healthz", nil)
	if err != nil {
		return errors.New("invalid address")
	}
	client := &http.Client{
		Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		var ne net.Error
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
			return fmt.Errorf("timed out after %s waiting for the controller", timeout)
		}
		return fmt.Errorf("cannot reach the controller: %w", errors.Unwrap(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("not a steamcmd-serve controller (GET /healthz returned %d, expected 204)", resp.StatusCode)
	}
	return nil
}

// isPrivateHost reports whether a plain-http host is on a private network
// (loopback, RFC 1918, unique-local, link-local, localhost, *.local).
func isPrivateHost(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(strings.Trim(host, "[]"), "."))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") || strings.HasSuffix(h, ".local") {
		return true
	}
	ip := net.ParseIP(h)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
}

// insecureRemote reports whether base is plain http to a non-private host.
func insecureRemote(base string) bool {
	u, err := url.Parse(base)
	return err == nil && u.Scheme == "http" && !isPrivateHost(u.Hostname())
}

type config struct {
	ControllerURL string `json:"controllerURL"`
}

type configStore struct {
	dirFunc func() (string, error) // os.UserConfigDir in production
}

func (s configStore) path() (string, error) {
	base, err := s.dirFunc()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, configSubdir, configFile), nil
}

func (s configStore) save(controllerURL string) error {
	p, err := s.path()
	if err != nil {
		return err
	}
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(config{ControllerURL: controllerURL})
	if err != nil {
		return err
	}
	if err := os.WriteFile(p, data, 0o600); err != nil {
		return err
	}
	return os.Chmod(p, 0o600) // enforce even if the file pre-existed
}

// load returns the saved URL, or "" if none/unreadable/invalid.
func (s configStore) load() string {
	p, err := s.path()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	var c config
	if json.Unmarshal(data, &c) != nil {
		return ""
	}
	u, err := normalizeURL(c.ControllerURL)
	if err != nil {
		return ""
	}
	return u
}
