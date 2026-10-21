// Command controller serves the steamcmd-serve web UI and API.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"steamcmd-serve/games"
	"steamcmd-serve/internal/api"
	"steamcmd-serve/internal/auth"
	"steamcmd-serve/internal/catalog"
	"steamcmd-serve/internal/instance"
	"steamcmd-serve/internal/jobs"
	"steamcmd-serve/internal/runtime"
	"steamcmd-serve/internal/secrets"
	"steamcmd-serve/internal/store"
)

func main() {
	if err := run(); err != nil {
		slog.Error("controller exited", "err", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	catalogGames, err := catalog.Load(games.FS)
	if err != nil {
		return err
	}
	slog.Info("loaded games", "ids", catalog.IDs(catalogGames))

	dataDir := envOr("DATA_DIR", "/data")
	puid, err := envInt("PUID", 1000)
	if err != nil {
		return err
	}
	pgid, err := envInt("PGID", 1000)
	if err != nil {
		return err
	}
	backupEvery, err := time.ParseDuration(envOr("BACKUP_INTERVAL", "6h"))
	if err != nil {
		return fmt.Errorf("BACKUP_INTERVAL: %w", err)
	}
	hostDataRoot := os.Getenv("HOST_DATA_ROOT")
	if hostDataRoot == "" {
		return errors.New("HOST_DATA_ROOT is required: the host path of DATA_DIR (see docs/adr/0001-architecture.md §7)")
	}

	box, err := secrets.Open(filepath.Join(dataDir, "secret.key"))
	if err != nil {
		return err
	}
	st, err := store.Open(filepath.Join(dataDir, "controller.db"))
	if err != nil {
		return err
	}
	defer st.Close()
	rt, err := runtime.New()
	if err != nil {
		return err
	}
	defer rt.Close()
	if err := rt.Ping(ctx); err != nil {
		return fmt.Errorf("container engine not reachable (DOCKER_HOST): %w", err)
	}

	imageRepo := envOr("IMAGE_REPO", "ghcr.io/OWNER")
	imageTag := envOr("IMAGE_TAG", "latest")
	jt := jobs.NewTracker(ctx)
	svc, err := instance.New(st, rt, catalogGames, jt, instance.Options{
		DataDir:       dataDir,
		HostDataRoot:  hostDataRoot,
		GamesDir:      os.Getenv("GAMES_DIR"),
		HostGamesRoot: os.Getenv("HOST_GAMES_ROOT"),
		PUID:         puid,
		PGID:         pgid,
		TZ:           os.Getenv("TZ"),
		Images: map[string]string{
			"steamcmd-base": imageRepo + "/steamcmd-base:" + imageTag,
			"steamcmd-wine": imageRepo + "/steamcmd-wine:" + imageTag,
		},
		Secrets: box,
	})
	if err != nil {
		return err
	}
	if err := svc.Reconcile(ctx, true); err != nil {
		return fmt.Errorf("reconcile: %w", err)
	}
	go every(ctx, 15*time.Second, func() {
		if err := svc.Reconcile(ctx, false); err != nil && ctx.Err() == nil {
			slog.Warn("reconcile", "err", err)
		}
	})
	if backupEvery > 0 {
		go every(ctx, backupEvery, func() { svc.ScheduledBackups(ctx) })
	}

	authn, err := auth.New(st, strings.Split(os.Getenv("TRUSTED_PROXIES"), ","))
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	authn.Register(mux)
	api.New(svc).Register(mux)
	mux.Handle("/", spa(envOr("UI_DIR", "/ui")))

	srv := &http.Server{
		Addr:              envOr("LISTEN_ADDR", ":8080"),
		Handler:           securityHeaders(authn.Middleware(mux)),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(sctx)
	}()
	slog.Info("listening", "addr", srv.Addr)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	jt.Wait() // jobs see ctx cancelled; boot reconcile repairs interrupted ones
	return nil
}

// spa serves the built web UI from dir, falling back to index.html for client routes.
func spa(dir string) http.Handler {
	files := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		p := filepath.Join(dir, filepath.FromSlash(filepath.Clean("/"+r.URL.Path)))
		if fi, err := os.Stat(p); err != nil || fi.IsDir() && r.URL.Path != "/" {
			http.ServeFile(w, r, filepath.Join(dir, "index.html"))
			return
		}
		files.ServeHTTP(w, r)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; img-src 'self' data:; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

func every(ctx context.Context, d time.Duration, fn func()) {
	t := time.NewTicker(d)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fn()
		}
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%s must be a non-negative integer", key)
	}
	return n, nil
}
