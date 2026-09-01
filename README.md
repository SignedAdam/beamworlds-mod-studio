# BeamWorlds Mod Studio

Native Wails desktop application for indexing local BeamNG ZIP mods, editing isolated ModMaker workspaces, collaborating with Virgil, and launching isolated mod profiles.

## Configure and run

1. Copy `config.example.json` at the repository root to `config.json`.
2. Set the BeamNG user-folder, library, active-mod, and installation paths.
3. Run `Run BeamWorlds Mod Studio.cmd` after building the executable.

The launcher resolves paths relative to the repository. Persistent state lives under the ignored `studio-data/` directory: SQLite metadata, content-addressed images, editable workspaces, launch profiles, and immutable exports.

## Safety model

- Library archives are read in place and never unpacked during scanning.
- ModMaker extracts a separate workspace and records the source SHA-256.
- Editor and OMP tools can access only workspace-relative paths.
- Unsaved editor drafts persist in SQLite and are recovered after restart.
- Export validates the workspace, checks the source checksum before and after packing, writes atomically, and registers a new immutable ZIP.
- Test installs use the `modstudio-test-*.zip` namespace and verify their checksum before launch.
- Profiles materialize selected archives into application-owned BeamNG user folders and launch with `-userpath`; existing active mods are never moved or deleted.

## Build

Install Go, Node.js, and Wails v3, then run from the repository root:

```text
cd studio/BeamNGModStudio/frontend
npm ci
cd ..
wails3 build
```

The production executable is written to `studio/BeamNGModStudio/bin/beamngmodstudio.exe`.
