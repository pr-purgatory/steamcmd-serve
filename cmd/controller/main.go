// Command controller serves the steamcmd-serve web UI and API.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"steamcmd-serve/games"
	"steamcmd-serve/internal/catalog"
	"steamcmd-serve/internal/instance"
	"steamcmd-serve/internal/runtime"
	"steamcmd-serve/internal/store"
)

func main() {
	if err := run(); err != nil {
		slog.Error("controller exited", "err", err)
		os.Exit(1)
	}
}

func run() error {
	ctx := context.Background()
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
	imageRepo := envOr("IMAGE_REPO", "ghcr.io/OWNER")
	imageTag := envOr("IMAGE_TAG", "latest")
	opt := instance.Options{
		DataDir:      dataDir,
		HostDataRoot: os.Getenv("HOST_DATA_ROOT"),
		PUID:         puid,
		PGID:         pgid,
		TZ:           os.Getenv("TZ"),
		Images: map[string]string{
			"steamcmd-base": imageRepo + "/steamcmd-base:" + imageTag,
			"steamcmd-wine": imageRepo + "/steamcmd-wine:" + imageTag,
		},
	}
	if opt.HostDataRoot == "" {
		return errors.New("HOST_DATA_ROOT is required: the host path of DATA_DIR (see docs/adr/0001-architecture.md §7)")
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
	svc, err := instance.New(st, rt, catalogGames, opt)
	if err != nil {
		return err
	}
	if err := svc.Reconcile(ctx); err != nil {
		return fmt.Errorf("reconcile: %w", err)
	}

	addr := envOr("LISTEN_ADDR", ":8080")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	slog.Info("listening", "addr", addr)
	if err := http.ListenAndServe(addr, mux); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
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
