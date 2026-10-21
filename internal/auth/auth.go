// Package auth provides first-run setup, login sessions and the API auth middleware:
// a single local admin (argon2id), an HttpOnly session cookie, and a CSRF token
// required on every state-changing API request (ADR 0001 §6).
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"

	"steamcmd-serve/internal/store"
)

const (
	// CookieName matches the OpenAPI securityScheme.
	CookieName = "scs_session"
	// CSRFHeader carries the session's CSRF token on unsafe requests.
	CSRFHeader = "X-CSRF-Token"

	sessionTTL     = 7 * 24 * time.Hour
	minPasswordLen = 12
	maxFailures    = 5 // per client per window
	failureWindow  = time.Minute
	maxHashing     = 2 // concurrent argon2 computations (64 MiB each)
)

// argon2id parameters (OWASP: m ≥ 19 MiB, t ≥ 2).
const (
	argonTime    = 2
	argonMemory  = 64 * 1024
	argonThreads = 2
	argonKeyLen  = 32
)

// Auth serves the auth endpoints and wraps the API with the session check.
type Auth struct {
	store   *store.Store
	trusted []netip.Prefix // reverse proxies whose X-Forwarded-* headers are believed

	mu       sync.Mutex
	failures map[string][]time.Time
	hashing  chan struct{}
}

// New returns an Auth. trustedProxies are CIDRs (or IPs) of reverse proxies.
func New(st *store.Store, trustedProxies []string) (*Auth, error) {
	a := &Auth{store: st, failures: map[string][]time.Time{}, hashing: make(chan struct{}, maxHashing)}
	for _, p := range trustedProxies {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !strings.Contains(p, "/") {
			addr, err := netip.ParseAddr(p)
			if err != nil {
				return nil, fmt.Errorf("TRUSTED_PROXIES: %w", err)
			}
			p = netip.PrefixFrom(addr, addr.BitLen()).String()
		}
		prefix, err := netip.ParsePrefix(p)
		if err != nil {
			return nil, fmt.Errorf("TRUSTED_PROXIES: %w", err)
		}
		a.trusted = append(a.trusted, prefix.Masked())
	}
	return a, nil
}

// Register adds /api/v1/setup and /api/v1/auth/* routes.
func (a *Auth) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/setup", a.setupStatus)
	mux.HandleFunc("POST /api/v1/setup", a.setup)
	mux.HandleFunc("POST /api/v1/auth/login", a.login)
	mux.HandleFunc("POST /api/v1/auth/logout", a.logout)
	mux.HandleFunc("GET /api/v1/auth/session", a.session)
}

type ctxKey struct{}

// Username returns the logged-in user set by Middleware.
func Username(ctx context.Context) string {
	s, _ := ctx.Value(ctxKey{}).(*store.Session)
	if s == nil {
		return ""
	}
	return s.Username
}

func isPublic(r *http.Request) bool {
	switch r.URL.Path {
	case "/api/v1/setup":
		return r.Method == http.MethodGet || r.Method == http.MethodPost
	case "/api/v1/auth/login":
		return r.Method == http.MethodPost
	}
	return false
}

func isSafe(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}

// Middleware requires a valid session for /api/ paths (except setup and login) and a
// matching CSRF token on unsafe methods. Other paths (the web UI, /healthz) pass through.
func (a *Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		// JSON-only bodies on public endpoints: an HTML form cannot send them cross-site
		// without a CORS preflight, which closes login CSRF.
		if !isSafe(r.Method) && isPublic(r) && !hasJSONBody(r) {
			writeError(w, http.StatusUnsupportedMediaType, "bad_request", "Content-Type must be application/json")
			return
		}
		if isPublic(r) {
			next.ServeHTTP(w, r)
			return
		}
		sess, err := a.sessionFrom(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "login required")
			return
		}
		if !isSafe(r.Method) {
			got := r.Header.Get(CSRFHeader)
			if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(sess.CSRFToken)) != 1 {
				writeError(w, http.StatusForbidden, "csrf", "missing or invalid "+CSRFHeader)
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, sess)))
	})
}

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (a *Auth) setupStatus(w http.ResponseWriter, r *http.Request) {
	n, err := a.store.UserCount(r.Context())
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"required": n == 0})
}

func (a *Auth) setup(w http.ResponseWriter, r *http.Request) {
	c, ok := decodeCredentials(w, r)
	if !ok {
		return
	}
	if len(c.Username) == 0 || len(c.Username) > 64 || len([]rune(c.Password)) < minPasswordLen {
		writeError(w, http.StatusBadRequest, "invalid", fmt.Sprintf("username 1-64 characters, password at least %d characters", minPasswordLen))
		return
	}
	if n, err := a.store.UserCount(r.Context()); err != nil {
		internalError(w, err)
		return
	} else if n > 0 {
		writeError(w, http.StatusConflict, "conflict", "setup already completed")
		return
	}
	hash, err := a.hash(r.Context(), c.Password)
	if err != nil {
		internalError(w, err)
		return
	}
	if err := a.store.CreateFirstUser(r.Context(), c.Username, hash); errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, "conflict", "setup already completed")
		return
	} else if err != nil {
		internalError(w, err)
		return
	}
	slog.Info("admin account created", "username", c.Username)
	w.WriteHeader(http.StatusCreated)
}

func (a *Auth) login(w http.ResponseWriter, r *http.Request) {
	client := a.clientIP(r)
	if a.tooManyFailures(client) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many failed logins; try again in a minute")
		return
	}
	c, ok := decodeCredentials(w, r)
	if !ok {
		return
	}
	name, hash, err := a.store.PasswordHash(r.Context(), c.Username)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		internalError(w, err)
		return
	}
	if hash == "" {
		hash = dummyHash // same work for unknown users
	}
	valid, err := a.verify(r.Context(), c.Password, hash)
	if err != nil {
		internalError(w, err)
		return
	}
	if !valid || name == "" {
		a.recordFailure(client)
		slog.Warn("failed login", "client", client)
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid username or password")
		return
	}

	token, csrf := randomToken(), randomToken()
	sess := store.Session{TokenHash: hashToken(token), Username: name, CSRFToken: csrf, ExpiresAt: time.Now().Add(sessionTTL)}
	if err := a.store.CreateSession(r.Context(), sess); err != nil {
		internalError(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(sessionTTL.Seconds()),
		HttpOnly: true,
		Secure:   a.isHTTPS(r),
		SameSite: http.SameSiteLaxMode,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (a *Auth) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(CookieName); err == nil {
		if err := a.store.DeleteSession(r.Context(), hashToken(c.Value)); err != nil {
			internalError(w, err)
			return
		}
	}
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: a.isHTTPS(r), SameSite: http.SameSiteLaxMode})
	w.WriteHeader(http.StatusNoContent)
}

func (a *Auth) session(w http.ResponseWriter, r *http.Request) {
	sess, _ := r.Context().Value(ctxKey{}).(*store.Session)
	if sess == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "login required")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"username": sess.Username, "csrfToken": sess.CSRFToken})
}

func (a *Auth) sessionFrom(r *http.Request) (*store.Session, error) {
	c, err := r.Cookie(CookieName)
	if err != nil || c.Value == "" {
		return nil, store.ErrNotFound
	}
	return a.store.GetSession(r.Context(), hashToken(c.Value))
}

// ---- passwords -----------------------------------------------------------------

// dummyHash is verified against for unknown usernames so timing does not reveal them.
var dummyHash = encodeHash(make([]byte, 16), make([]byte, argonKeyLen))

func (a *Auth) hash(ctx context.Context, password string) (string, error) {
	salt := make([]byte, 16)
	rand.Read(salt)
	key, err := a.argon(ctx, password, salt)
	if err != nil {
		return "", err
	}
	return encodeHash(salt, key), nil
}

func (a *Auth) verify(ctx context.Context, password, encoded string) (bool, error) {
	var m, t uint32
	var p uint8
	var saltB64, keyB64 string
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return false, errors.New("unsupported password hash")
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false, err
	}
	saltB64, keyB64 = parts[4], parts[5]
	salt, err := base64.RawStdEncoding.DecodeString(saltB64)
	if err != nil {
		return false, err
	}
	want, err := base64.RawStdEncoding.DecodeString(keyB64)
	if err != nil {
		return false, err
	}
	if m != argonMemory || t != argonTime || p != argonThreads {
		return false, errors.New("password hash parameters changed; reset required")
	}
	got, err := a.argon(ctx, password, salt)
	if err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// argon bounds concurrent hashing so login floods cannot exhaust memory.
func (a *Auth) argon(ctx context.Context, password string, salt []byte) ([]byte, error) {
	select {
	case a.hashing <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-a.hashing }()
	return argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen), nil
}

func encodeHash(salt, key []byte) string {
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key))
}

// ---- rate limiting & client identity ----------------------------------------------

func (a *Auth) tooManyFailures(client string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	cutoff := time.Now().Add(-failureWindow)
	recent := a.failures[client][:0]
	for _, t := range a.failures[client] {
		if t.After(cutoff) {
			recent = append(recent, t)
		}
	}
	if len(recent) == 0 {
		delete(a.failures, client)
	} else {
		a.failures[client] = recent
	}
	return len(recent) >= maxFailures
}

func (a *Auth) recordFailure(client string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.failures[client] = append(a.failures[client], time.Now())
}

func (a *Auth) fromTrustedProxy(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	return a.isTrusted(addr.Unmap())
}

func (a *Auth) isTrusted(addr netip.Addr) bool {
	for _, p := range a.trusted {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// clientIP is the remote address, or for trusted proxies the right-most untrusted
// X-Forwarded-For hop.
func (a *Auth) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if !a.fromTrustedProxy(r) {
		return host
	}
	hops := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		addr, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break
		}
		if !a.isTrusted(addr.Unmap()) {
			return addr.String()
		}
	}
	return host
}

func (a *Auth) isHTTPS(r *http.Request) bool {
	return r.TLS != nil || (a.fromTrustedProxy(r) && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https"))
}

// ---- helpers -------------------------------------------------------------------------

func randomToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func hasJSONBody(r *http.Request) bool {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && mt == "application/json"
}

func decodeCredentials(w http.ResponseWriter, r *http.Request) (credentials, bool) {
	var c credentials
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "body must be {\"username\": string, \"password\": string}")
		return c, false
	}
	return c, true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"code": code, "message": msg})
}

func internalError(w http.ResponseWriter, err error) {
	slog.Error("auth", "err", err)
	writeError(w, http.StatusInternalServerError, "internal", "internal error")
}
