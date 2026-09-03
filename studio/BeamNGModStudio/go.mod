module github.com/SignedAdam/beamng-mod-studio

go 1.25.0

require github.com/wailsapp/wails/v3 v3.0.0-beta.16

require (
	github.com/SignedAdam/beamworlds-modkit v0.0.0
	github.com/google/uuid v1.6.0
	github.com/sergi/go-diff v1.4.0
	golang.org/x/sys v0.47.0
	modernc.org/sqlite v1.57.0
)

replace github.com/SignedAdam/beamworlds-modkit => ../../modkit

require (
	github.com/adrg/xdg v0.5.3 // indirect
	github.com/alchemy/json5 v0.2.0 // indirect
	github.com/coder/websocket v1.8.14 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/go-ole/go-ole v1.3.0 // indirect
	github.com/godbus/dbus/v5 v5.2.2 // indirect
	github.com/jchv/go-winloader v0.0.0-20250406163304-c1995be93bd1 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	modernc.org/libc v1.74.4 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
)
