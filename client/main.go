package main

import (
	"embed"
	"io/fs"
	"log"
	"runtime"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/menu/keys"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

//go:embed all:frontend
var frontend embed.FS

func buildMenu(app *App) *menu.Menu {
	m := menu.NewMenu()
	if runtime.GOOS == "darwin" {
		m.Append(menu.AppMenu())
		m.Append(menu.EditMenu())
	}
	ctl := m.AddSubmenu("Controller")
	ctl.AddText("Change controller…", nil, func(*menu.CallbackData) { app.changeController() })
	ctl.AddText("Reload", keys.CmdOrCtrl("r"), func(*menu.CallbackData) { app.reload() })
	if runtime.GOOS != "darwin" {
		ctl.AddSeparator()
		ctl.AddText("Quit", keys.CmdOrCtrl("q"), func(*menu.CallbackData) { wailsruntime.Quit(app.ctx) })
	}
	return m
}

func main() {
	assets, err := fs.Sub(frontend, "frontend")
	if err != nil {
		log.Fatal(err)
	}
	app := NewApp()
	err = wails.Run(&options.App{
		Title:     "steamcmd-serve",
		Width:     1280,
		Height:    820,
		MinWidth:  900,
		MinHeight: 600,
		Menu:      buildMenu(app),
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		OnStartup:  app.startup,
		OnDomReady: app.domReady,
		Bind:       []interface{}{app},
	})
	if err != nil {
		log.Fatal(err)
	}
}
