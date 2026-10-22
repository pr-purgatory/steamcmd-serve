package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// App is the only bound type; its only exported method is Connect.
type App struct {
	ctx   context.Context
	store configStore

	mu          sync.Mutex
	pendingInit bool // next DOM-ready is the embedded connect page
	skipAuto    bool // consumed once by the next connect-page load
}

func NewApp() *App {
	return &App{store: configStore{dirFunc: os.UserConfigDir}, pendingInit: true}
}

func (a *App) startup(ctx context.Context) { a.ctx = ctx }

// Connect validates the controller, saves it and returns the normalized URL.
func (a *App) Connect(url string) (string, error) {
	u, err := normalizeURL(url)
	if err != nil {
		return "", err
	}
	if err := checkHealth(u, healthTimeout); err != nil {
		return "", err
	}
	if err := a.store.save(u); err != nil {
		return "", fmt.Errorf("connected, but could not save settings: %w", err)
	}
	return u, nil
}

// domReady runs after every page load. Only the first load of the embedded
// connect page (startup, or after "Change controller") is initialised; loads
// of the controller's own pages are ignored.
func (a *App) domReady(ctx context.Context) {
	a.mu.Lock()
	if !a.pendingInit {
		a.mu.Unlock()
		return
	}
	a.pendingInit = false
	skip := a.skipAuto
	a.skipAuto = false
	a.mu.Unlock()

	saved := a.store.load()
	state := map[string]string{"url": saved}
	if saved != "" && !skip {
		if err := checkHealth(saved, healthTimeout); err == nil {
			b, _ := json.Marshal(saved)
			runtime.WindowExecJS(ctx, `window.location.href = `+string(b)+`;`)
			return
		} else {
			state["error"] = err.Error()
		}
	}
	b, _ := json.Marshal(state)
	runtime.WindowExecJS(ctx, `if (window.steamcmdInit) window.steamcmdInit(`+string(b)+`);`)
}

// changeController returns to the embedded connect page without auto-connect.
func (a *App) changeController() {
	a.mu.Lock()
	a.pendingInit = true
	a.skipAuto = true
	a.mu.Unlock()
	runtime.WindowReloadApp(a.ctx)
}

func (a *App) reload() { runtime.WindowReload(a.ctx) }
