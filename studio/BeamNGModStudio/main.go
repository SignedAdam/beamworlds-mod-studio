package main

import (
	"embed"
	"log"

	"github.com/wailsapp/wails/v3/pkg/application"
)

//go:embed all:frontend/dist
var assets embed.FS

func init() {
	application.RegisterEvent[ScanProgress]("library:scan")
	application.RegisterEvent[LibraryItem]("library:item")
	application.RegisterEvent[AgentActivity]("agent:event")
}

func main() {
	config, err := LoadAppConfig()
	if err != nil {
		log.Fatalf("load application configuration: %v", err)
	}
	store, err := OpenStore(config.DatabasePath)
	if err != nil {
		log.Fatalf("open application database: %v", err)
	}

	var app *application.App
	emit := func(name string, payload any) {
		if app != nil {
			app.Event.Emit(name, payload)
		}
	}
	service := NewAppService(config, store, emit)
	embedded := application.AssetFileServerFS(assets)
	app = application.New(application.Options{
		Name:        "BeamWorlds Mod Studio",
		Description: "Local-first BeamNG mod library and ModMaker",
		Services: []application.Service{
			application.NewService(service),
		},
		Assets: application.AssetOptions{
			Handler: &cacheAssetHandler{store: store, cacheDir: config.ImageCacheDir, fallback: embedded},
		},
		Windows: application.WindowsOptions{
			AdditionalBrowserArgs: debugBrowserArgs(),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})
	app.OnShutdown(func() {
		service.shutdown()
		if err := store.Close(); err != nil {
			log.Printf("close application database: %v", err)
		}
	})

	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             "main",
		Title:            "BeamWorlds Mod Studio",
		Width:            1500,
		Height:           940,
		MinWidth:         1120,
		MinHeight:        720,
		BackgroundColour: application.NewRGB(11, 14, 20),
		URL:              "/",
		Mac: application.MacWindow{
			InvisibleTitleBarHeight: 48,
			Backdrop:                application.MacBackdropTranslucent,
			TitleBar:                application.MacTitleBarHiddenInset,
		},
	})

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
