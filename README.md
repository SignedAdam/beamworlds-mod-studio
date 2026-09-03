# BeamWorlds Mod Studio

Native Wails desktop application for indexing local BeamNG ZIP mods, editing isolated ModMaker workspaces, collaborating with Virgil, and launching exact mod profiles while preserving normal BeamNG settings, controls, and saves.

## Run

For this built checkout:

1. Double-click `Run BeamWorlds Mod Studio.cmd`.
2. On first launch, review the automatically detected BeamNG installation and user folder.
3. Choose the mod library and BeamWorlds staging location, then save the setup.

The CMD file is the only supported launcher. It starts `studio/BeamNGModStudio/bin/beamngmodstudio.exe`; the first-run wizard persists machine-local paths outside version control. The chosen Studio storage directory contains SQLite metadata, content-addressed images, editable workspaces, mod-profile cache, and immutable exports.

`config.example.json` remains available for portable or managed deployments. The executable is generated build output and is not committed. A fresh clone must be built once before the launcher can run it.

## Safety model

- Library archives are read in place and never unpacked during scanning.
- ModMaker extracts a separate workspace and records the source SHA-256.
- Editor and Virgil host tools can access only workspace-relative paths.
- Unsaved editor drafts persist in SQLite and are recovered after restart.
- Export validates the workspace, checks the source checksum before and after packing, writes atomically, and registers a new immutable ZIP.
- Test installs use the `modstudio-test-*.zip` namespace and verify their checksum before launch.
- Mod profiles combine reusable presets with individually selected mods. External archives are hardlinked or copied into `current/mods/beamworlds-managed`, BeamNG’s native `db.json` active state is backed up and updated atomically, normal settings/controls/saves remain shared, and the pre-profile mod selection can be restored from the app.

## Build a fresh clone

Install Go, Node.js, and Wails v3, then run from the repository root:

```text
cd studio/BeamNGModStudio/frontend
npm ci
cd ..
wails3 build
```

The production executable is written to `studio/BeamNGModStudio/bin/beamngmodstudio.exe`.
