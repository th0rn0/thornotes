package main

import (
	_ "embed"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"runtime"

	"github.com/th0rn0/thornotes-desktop/internal/core"
	webview "github.com/webview/webview_go"
)

func init() {
	runtime.LockOSThread()
}

//go:embed setup.html
var setupHTML []byte

//go:embed error.html
var errorHTML []byte

func main() {
	cfg, exists, err := core.LoadConfig()
	if err != nil {
		log.Printf("load config: %v", err)
		cfg = &core.Config{ServerURL: "http://localhost:8080"}
		exists = false
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("listen local: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	localBase := fmt.Sprintf("http://127.0.0.1:%d", port)

	mux := http.NewServeMux()
	mux.HandleFunc("/setup.html", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(setupHTML)
	})
	mux.HandleFunc("/error.html", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(errorHTML)
	})
	go func() { _ = http.Serve(ln, mux) }()

	w := webview.New(false)
	defer w.Destroy()
	w.SetTitle("Thornotes")
	w.SetSize(1280, 800, webview.HintNone)

	_ = w.Bind("goGetConfig", func() core.Config {
		return *cfg
	})

	_ = w.Bind("goSaveConfig", func(serverURL string, autostart bool) string {
		newCfg := &core.Config{ServerURL: serverURL, Autostart: autostart}
		if err := core.SaveConfig(newCfg); err != nil {
			return err.Error()
		}
		if err := core.SetAutostart(autostart); err != nil {
			return err.Error()
		}
		cfg = newCfg
		w.Dispatch(func() { w.Navigate(serverURL) })
		return ""
	})

	_ = w.Bind("goRetryConnect", func() bool {
		if core.CheckServer(cfg.ServerURL) {
			w.Dispatch(func() { w.Navigate(cfg.ServerURL) })
			return true
		}
		return false
	})

	_ = w.Bind("goChangeURL", func() {
		cfg.ServerURL = ""
		_ = core.SaveConfig(cfg)
		w.Dispatch(func() { w.Navigate(localBase + "/setup.html") })
	})

	if !exists || cfg.ServerURL == "" {
		w.Navigate(localBase + "/setup.html")
	} else if core.CheckServer(cfg.ServerURL) {
		w.Navigate(cfg.ServerURL)
	} else {
		w.Navigate(localBase + "/error.html?url=" + url.QueryEscape(cfg.ServerURL))
	}

	w.Run()
}
