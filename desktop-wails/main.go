package main

import (
	"embed"
	"log"

	"github.com/getlantern/systray"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/logger"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed all:frontend
var assets embed.FS

func main() {
	app := NewApp()

	// Systray runs in a goroutine on Linux and Windows.
	// On macOS, systray requires the main thread — for production macOS builds,
	// swap: run wails.Run in a goroutine and systray.Run on the main thread.
	go func() {
		defer func() {
			if r := recover(); r != nil {
				// Tray library unavailable (e.g. no system indicator on this desktop).
				// App continues without a tray.
				log.Printf("thornotes-wails: system tray unavailable: %v", r)
			}
		}()
		systray.Run(app.onTrayReady, app.onTrayExit)
	}()

	err := wails.Run(&options.App{
		Title:             "thornotes",
		Width:             1280,
		Height:            840,
		MinWidth:          480,
		MinHeight:         400,
		AssetServer:       &assetserver.Options{Assets: assets},
		BackgroundColour:  &options.RGBA{R: 30, G: 30, B: 30, A: 255},
		OnStartup:         app.startup,
		OnShutdown:        app.shutdown,
		Bind:              []interface{}{app},
		LogLevel:          logger.ERROR,
		HideWindowOnClose: true,
	})
	if err != nil {
		log.Fatal(err)
	}
}
