package main

import (
	"context"
	"embed"
	"fmt"
	"log"
	"path/filepath"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

//go:embed all:frontend/dist
var assets embed.FS

func init() {
	application.RegisterEvent[ScanProgress]("library:scan")
	application.RegisterEvent[LibraryItem]("library:item")
	application.RegisterEvent[AgentActivity]("agent:event")
	application.RegisterEvent[PlayProgress]("play:progress")
	application.RegisterEvent[VirusScanProgress]("virus:scan")
	application.RegisterEvent[AIConnectionEvent]("ai:connection")
	application.RegisterEvent[StorageProgress]("storage:progress")
	application.RegisterEvent[LibraryUpdateEvent]("mod:library")
	application.RegisterEvent[GameStatus]("game:status")
	application.RegisterEvent[NewModsReview]("mods:detected")
}

func main() {
	config, err := LoadAppConfig()
	if err != nil {
		log.Fatalf("load application configuration: %v", err)
	}
	var store *Store
	if strings.TrimSpace(config.LibraryDir) == "" {
		store, err = OpenStore(config.DatabasePath)
	} else {
		store, err = OpenStore(config.DatabasePath, filepath.Join(config.LibraryDir, "catalog.json"))
	}
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
	service.modImportMu.Lock()
	if err := service.reconcileStorageJournals(context.Background()); err != nil {
		log.Printf("archive storage recovery pending: %v", err)
	} else if err := service.recoverArchiveDeployment(context.Background()); err != nil {
		log.Printf("archive deployment recovery pending: %v", err)
	} else {
		if err := service.adoptExistingManagedEntries(context.Background()); err != nil {
			log.Printf("managed archive ownership adoption failed: %v", err)
		}
		service.reportCollectionMirrorRetirement(context.Background())
		// One-time cutover of the old beamworlds-managed layout.
		if err := service.cutoverLegacyManagedDir(context.Background()); err != nil {
			service.playProfileWarning = appendPlayWarning(service.playProfileWarning, fmt.Sprintf("Play profile cutover: %v", err))
			log.Printf("play profile cutover: %v", err)
		}
		// Sync play profile (junctions, migrate data).
		if err := service.syncPlayProfile(context.Background()); err != nil {
			service.playProfileWarning = appendPlayWarning(service.playProfileWarning, fmt.Sprintf("Play profile sync: %v", err))
			log.Printf("play profile sync: %v", err)
		}
		// Harvest session downloads from the play profile (only while game is stopped).
		if running, runErr := service.currentGameRunning(); runErr == nil && !running {
			if playRoot, err := playUserPath(config); err == nil {
				if _, harvestErr := service.reconcileProfileSessionDownloads(context.Background(), playRoot); harvestErr != nil {
					log.Printf("play profile: harvest session downloads: %v", harvestErr)
				}
			}
		}
	}
	service.modImportMu.Unlock()
	// Edited mods whose library file an older Studio replaced join history.
	service.resumeLibrarySyncs(context.Background())
	// Start the game monitor (process detection, folder polling, exit harvest).
	monitor := newGameMonitor(service)
	service.monitorInstance = monitor
	monitor.refreshArchiveLinks()
	monitor.start()
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

	savedWindow, err := store.loadWindowState(context.Background())
	if err != nil {
		log.Printf("read window size: %v", err)
	}
	// A size saved on a larger or since-disconnected monitor must still fit.
	if primary := app.Screen.GetPrimary(); primary != nil && primary.WorkArea.Width > 0 && primary.WorkArea.Height > 0 {
		savedWindow.Width = min(savedWindow.Width, primary.WorkArea.Width)
		savedWindow.Height = min(savedWindow.Height, primary.WorkArea.Height)
	}
	startState := application.WindowStateNormal
	if savedWindow.Maximised {
		startState = application.WindowStateMaximised
	}
	window := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             "main",
		Title:            "BeamWorlds Mod Studio",
		Width:            savedWindow.Width,
		Height:           savedWindow.Height,
		MinWidth:         minimumWindowWidth,
		MinHeight:        minimumWindowHeight,
		StartState:       startState,
		BackgroundColour: application.NewRGB(11, 14, 20),
		URL:              "/",
		Mac: application.MacWindow{
			InvisibleTitleBarHeight: 48,
			Backdrop:                application.MacBackdropTranslucent,
			TitleBar:                application.MacTitleBarHiddenInset,
		},
	})
	windowSaver := newWindowStateSaver(savedWindow, windowStateSaveDelay,
		func() (int, int, bool, bool) {
			width, height := window.Size()
			return width, height, window.IsMaximised(), window.IsMinimised()
		},
		func(state windowState) error { return store.saveWindowState(context.Background(), state) },
	)
	window.OnWindowEvent(events.Common.WindowDidResize, func(*application.WindowEvent) { windowSaver.resized() })
	// Closing during the countdown still keeps the latest size.
	window.RegisterHook(events.Common.WindowClosing, func(*application.WindowEvent) { windowSaver.flush() })

	if err := app.Run(); err != nil {
		monitor.stop()
		log.Fatal(err)
	}
	monitor.stop()
}
