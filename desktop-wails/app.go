package main

import (
	"context"
	"log"
	"os"
	"os/exec"

	"github.com/getlantern/systray"
	"github.com/th0rn0/thornotes-desktop-wails/internal/config"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// SaveResult is returned to the frontend JS after SaveConfig is called.
type SaveResult struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// App is the Wails application struct. Public methods are automatically bound
// to the frontend under window.go.main.App.
type App struct {
	ctx        context.Context
	cfg        config.AppConfig
	configPath string
}

func NewApp() *App {
	dir, _ := os.UserConfigDir()
	return &App{
		configPath: dir + "/thornotes-wails/config.json",
	}
}

// startup is called by Wails when the window is ready.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	cfg, err := config.Load(a.configPath)
	if err != nil {
		log.Printf("thornotes-wails: failed to load config: %v", err)
	}
	a.cfg = cfg
	if a.cfg.ServerURL != "" {
		runtime.Navigate(ctx, a.cfg.ServerURL)
	}
}

// shutdown is called by Wails just before the app exits.
func (a *App) shutdown(_ context.Context) {
	systray.Quit()
}

// ── Frontend-bound methods ────────────────────────────────────────────────────

// GetConfig returns the persisted config to the setup page.
func (a *App) GetConfig() config.AppConfig {
	return a.cfg
}

// SaveConfig validates and persists the server URL, then navigates the webview.
func (a *App) SaveConfig(serverURL string) SaveResult {
	u, err := config.ValidateServerURL(serverURL)
	if err != nil {
		return SaveResult{OK: false, Error: err.Error()}
	}
	a.cfg.ServerURL = u
	if err := config.Save(a.configPath, a.cfg); err != nil {
		return SaveResult{OK: false, Error: "Failed to save config: " + err.Error()}
	}
	runtime.Navigate(a.ctx, u)
	return SaveResult{OK: true}
}

// ── System tray ───────────────────────────────────────────────────────────────

func (a *App) onTrayReady() {
	systray.SetTitle("thornotes")
	systray.SetTooltip("thornotes")

	mOpen := systray.AddMenuItem("Open thornotes", "Show the thornotes window")
	systray.AddSeparator()
	mChange := systray.AddMenuItem("Change server…", "Connect to a different server")
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit thornotes", "Exit the application")

	go func() {
		for {
			select {
			case <-mOpen.ClickedCh:
				if a.ctx != nil {
					runtime.WindowShow(a.ctx)
					runtime.WindowUnminimise(a.ctx)
					runtime.WindowFocus(a.ctx)
				}
			case <-mChange.ClickedCh:
				// Clear saved URL and restart — simplest way to show setup screen
				// from an external URL context (X-Frame-Options: DENY prevents iframes;
				// Wails has no cross-platform "navigate back to embedded assets" API).
				a.cfg.ServerURL = ""
				if err := config.Save(a.configPath, a.cfg); err != nil {
					log.Printf("thornotes-wails: failed to clear config: %v", err)
				}
				if exe, err := os.Executable(); err == nil {
					_ = exec.Command(exe).Start()
				}
				if a.ctx != nil {
					runtime.Quit(a.ctx)
				}
				return
			case <-mQuit.ClickedCh:
				if a.ctx != nil {
					runtime.Quit(a.ctx)
				}
				return
			}
		}
	}()
}

func (a *App) onTrayExit() {}
