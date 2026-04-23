package main

import (
	_ "embed"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"time"

	webview "github.com/webview/webview_go"
)

func init() {
	runtime.LockOSThread()
}

//go:embed setup.html
var setupHTML []byte

//go:embed error.html
var errorHTML []byte

func checkServer(serverURL string) bool {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(serverURL)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode < 500
}

func main() {
	cfg, exists, err := loadConfig()
	if err != nil {
		log.Printf("load config: %v", err)
		cfg = &Config{ServerURL: "http://localhost:8080"}
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

	_ = w.Bind("goGetConfig", func() Config {
		return *cfg
	})

	_ = w.Bind("goSaveConfig", func(serverURL string, autostart bool) string {
		newCfg := &Config{ServerURL: serverURL, Autostart: autostart}
		if err := saveConfig(newCfg); err != nil {
			return err.Error()
		}
		if err := setAutostart(autostart); err != nil {
			return err.Error()
		}
		cfg = newCfg
		w.Dispatch(func() { w.Navigate(serverURL) })
		return ""
	})

	_ = w.Bind("goRetryConnect", func() bool {
		if checkServer(cfg.ServerURL) {
			w.Dispatch(func() { w.Navigate(cfg.ServerURL) })
			return true
		}
		return false
	})

	_ = w.Bind("goChangeURL", func() {
		cfg.ServerURL = ""
		_ = saveConfig(cfg)
		w.Dispatch(func() { w.Navigate(localBase + "/setup.html") })
	})

	if !exists || cfg.ServerURL == "" {
		w.Navigate(localBase + "/setup.html")
	} else if checkServer(cfg.ServerURL) {
		w.Navigate(cfg.ServerURL)
	} else {
		w.Navigate(localBase + "/error.html?url=" + url.QueryEscape(cfg.ServerURL))
	}

	w.Run()
}
