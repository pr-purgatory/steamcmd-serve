// Command controller serves the steamcmd-serve web UI and API.
package main

import (
	"errors"
	"log/slog"
	"net/http"
	"os"

	"steamcmd-serve/games"
	"steamcmd-serve/internal/catalog"
)

func main() {
	if err := run(); err != nil {
		slog.Error("controller exited", "err", err)
		os.Exit(1)
	}
}

func run() error {
	catalogGames, err := catalog.Load(games.FS)
	if err != nil {
		return err
	}
	slog.Info("loaded games", "ids", catalog.IDs(catalogGames))

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
