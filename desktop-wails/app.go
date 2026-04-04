package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/getlantern/systray"
	"github.com/th0rn0/thornotes-desktop-wails/internal/config"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// Config is persisted to the OS user-data directory.
type Config struct {
	ServerURL string `json:"serverUrl"`
}

// SaveResult is returned to the frontend JS after SaveConfig is called.
type SaveResult struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// App is the Wails application struct. Public methods are automatically bound
// to the frontend under window.go.main.App.
type App struct {
	ctx        context.Context
	cfg        Config
	configPath string
}

func NewApp() *App {
	dir, _ := os.UserConfigDir()
	return &App{
		configPath: filepath.Join(dir, "thornotes-wails", "config.json"),
	}
}

// startup is called by Wails when the window is ready.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.loadConfig()
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
func (a *App) GetConfig() Config {
	return a.cfg
}

// SaveConfig validates and persists the server URL, then navigates the webview.
func (a *App) SaveConfig(serverURL string) SaveResult {
	u, err := config.ValidateServerURL(serverURL)
	if err != nil {
		return SaveResult{OK: false, Error: err.Error()}
	}
	a.cfg.ServerURL = u
	if err := a.persistConfig(); err != nil {
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
				if err := a.persistConfig(); err != nil {
					log.Printf("thornotes: failed to clear config: %v", err)
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

// ── Config persistence ────────────────────────────────────────────────────────

func (a *App) loadConfig() {
	data, err := os.ReadFile(a.configPath)
	if err != nil {
		return
	}
	_ = json.Unmarshal(data, &a.cfg)
}

func (a *App) persistConfig() error {
	if err := os.MkdirAll(filepath.Dir(a.configPath), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(a.cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(a.configPath, data, 0o644)
}
