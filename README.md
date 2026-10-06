# BeamWorlds Mod Studio

**Your BeamNG mods. Organized, editable, ready to play.**

[![Windows](https://img.shields.io/badge/Windows-x64_preview-orange?logo=windows)](#platform-support) [![Linux](https://img.shields.io/badge/Linux-not_release_ready-lightgrey?logo=linux)](#platform-support) [![macOS](https://img.shields.io/badge/macOS-not_release_ready-lightgrey?logo=apple)](#platform-support) [![Release](https://img.shields.io/badge/Release-v0.1.0--preview.1-orange)](https://github.com/SignedAdam/beamworlds-mod-studio/releases/tag/v0.1.0-preview.1) [![License](https://img.shields.io/badge/License-GPL--3.0--only-blue)](LICENSE) [![Built with](https://img.shields.io/badge/Built_with-Go_%2B_React_%2B_Wails-00ADD8)](#technical-details)

[Getting started](#getting-started) · [Downloads](https://github.com/SignedAdam/beamworlds-mod-studio/releases) · [Technical details](#technical-details) · [BeamWorlds website](https://beamworlds.b0rz.com)

Browse your mod library with previews and tags, organize collections, save the sets you want to play, and edit mods in a separate workspace. Virgil provides optional AI-assisted mod editing.

![BeamWorlds Mod Studio's Mod Library, showing previews, tags, and collection controls](.github/assets/mod-library.png)

> **Early preview — not a stable release.** Back up your mods and BeamNG user folder before testing. This Windows build is unsigned. GitHub's **Code → Download ZIP** contains source code, **not the app**; use the downloads below.

## Getting started

### 1. Get the app

**[Download the Windows preview](https://github.com/SignedAdam/beamworlds-mod-studio/releases/tag/v0.1.0-preview.1)** and open **Assets**. You do **not** need Go, Node.js, Git, or a terminal to use it.

| Download | How to run it |
| --- | --- |
| **`BeamWorldsModStudio-windows-amd64.zip` — recommended for this preview** | Right-click the ZIP → **Extract All**, open the extracted folder, then double-click **`beamngmodstudio.exe`**. Do not run it from inside the ZIP. |
| `BeamWorldsModStudio-windows-amd64-installer.exe` | Run the setup program, then open **BeamWorlds Mod Studio** from the Start menu. This package is built but has not yet been tested through a clean-machine install/uninstall cycle. |
| `SHA256SUMS` | Checksums for verifying the two downloads; not something to install. |

Keep the extracted license notices with the portable app. A source checkout's `Run BeamWorlds Mod Studio.cmd` only works after building from source; it is not a downloader.

### 2. Connect BeamNG

Close BeamNG before setting up or changing which mods it loads. On first launch, the app walks you through four steps:

| Step | What to choose |
| --- | --- |
| **BeamNG** | Your installed game folder and your existing BeamNG user folder. Review the detected locations; the game folder contains `Bin64/BeamNG.drive.x64.exe`. The user folder holds your settings, controls, and saves. |
| **Mods** | Your current BeamNG mods folder and the folder where you keep your mod ZIPs. Existing mods can stay where they are. |
| **Storage** | A folder for Studio's database, previews, editable projects, and exports. Keep **Automatic** deployment unless you specifically need independent copies. |
| **Review** | Check the locations, save, and let Studio restart. Wait for the initial library scan. |

If you are unsure where BeamNG stores your files, use the BeamNG launcher's user-folder controls to locate the existing user folder rather than guessing or creating a replacement.

### 3. Add, organize, play

1. Your configured folders are scanned automatically. Use **Add mod…** for additional ZIP files, or **Rescan mods** after installing files outside Studio.
2. Browse **Mod Library**, then organize mods into **Collections**.
3. In **Play**, choose your collections or **All mods**, then press **Play**. Save combinations as profiles if you want to reuse them.

Adding a mod to the library does not automatically enable it in the game. Studio changes the game's mod selection when you use Play; normal BeamNG settings, controls, and saves remain shared.

### Requirements and help

- **Windows x64 and a working BeamNG.drive installation** are the current testing target. BeamNG and mod files are not included.
- Windows needs the [Microsoft Edge WebView2 Runtime](https://developer.microsoft.com/en-us/microsoft-edge/webview2/). If Windows reports it missing or the app cannot create its window, install the official runtime and try again.
- **AI is optional for library management.** Virgil requires a supported AI connection configured in the app; provider subscriptions, limits, and charges are separate. Selected workspace content may be sent to that provider when you use AI.
- This preview is **unsigned**. If Windows shows an unknown-publisher warning, verify the download source and release checksum. Do not disable antivirus or system protections to run an unverified download.
- If setup cannot find the game, select the folder containing `Bin64`, not the executable itself. Keep BeamNG closed while Studio changes its active mods.
- For a problem report, include your Windows version, Studio build/release, what you clicked, and the error text. Remove account information, credentials, and private paths from screenshots and logs, then open a [GitHub Issue](https://github.com/SignedAdam/beamworlds-mod-studio/issues).

## Platform support

These badges describe readiness, not promises that a build or download exists.

| Platform | Current status | Public download |
| --- | --- | --- |
| **Windows x64** | Early preview. Production build, automated checks, and native startup/library smoke passed; broad clean-machine and real-game validation remain. | Portable ZIP and `.exe` installer in [Releases](https://github.com/SignedAdam/beamworlds-mod-studio/releases). |
| **Linux** | Not release-ready. Packaging templates exist, but game discovery/launch and the bundled AI runtime are not verified for Linux. | No supported AppImage or native package yet. |
| **macOS** | Not release-ready. Packaging templates are not evidence of a working BeamNG workflow or a signed/notarized application. | No supported `.dmg` yet. |

Windows preview release checks include first-run setup, library import, Play/recovery, and optional AI behavior. Linux/macOS need native game discovery, process safeguards, and runtime support before releases can be offered.

---

## User guide

## Add existing mods

Studio checks your library and configured BeamNG mods folder at every startup, including `mods/repo`, even when you have additional scan folders. Existing ZIPs are indexed in place; unchanged archives use cached scan results. Use **Rescan mods** for files installed while Studio is already open.

Choose **Add mod…** in the Mod Library to open the custom file explorer. Each opening starts in your Downloads folder; if Downloads is unavailable, the picker explains why and opens Home instead.

- Use Places, breadcrumbs, Back/Forward/Up, or paste a folder path to navigate. The eight most recently visited folders are remembered across restarts.
- Only folders and `.zip` files are shown. Filter the current folder or sort by name, size, or date modified.
- Select ZIPs using click, Ctrl/Command-click, Shift-click, or checkboxes. Keyboard arrows and Home/End navigate, Shift extends a range, Space toggles a file, and Ctrl/Command+A selects matching ZIPs. Double-click or Enter opens a folder.
- Choose **Add** to copy the selected ZIPs into your configured mod-library folder and index them immediately. Originals remain untouched. Different files with the same name receive numbered filenames; identical existing copies are reused.

Adding mods does not enable them in BeamNG or add them to a collection. If some files fail, successful imports stay in the library and the picker keeps the failed files selected with an explanation.

### Clean up duplicates

**Review duplicates** shows each set of duplicates as a card. Every version row lists the collections and tags it belongs to. Pick the version to keep (the suggested one is preselected), then choose what happens to the others:

- **Replace** deletes the other versions and gives their collections and tags to the kept version. The chips it gains are marked with **+**.
- **Remove** deletes the other versions and takes them out of their collections and tags. The kept version stays exactly as it is.
- **Keep both** / **Keep all** deletes nothing and stops flagging the set until its membership changes.
- For identical files of one mod, **Remove copies** deletes the extra files; collections and tags are not affected.

Each section can set every open card at once. Nothing changes until **Apply**; applying runs the chosen sets one after another in the background, so you can keep deciding (and apply again) while it works. Deleted files go to the Recycle Bin.

With **Replace**, shared collection entries merge: enabled wins, disabled-only entries stay disabled, and the keeper's existing position is preserved. Profiles follow their collections automatically. Old security scan results are not copied onto a different archive.

If a file is locked, the usage change still applies and the version stays in the library; **Check again** reloads the remaining duplicates so cleanup can be retried. A library that changed since the card was checked, or a kept version whose file is missing, blocks deletion for that set. Versions with ModMaker projects remain protected until their editable work is preserved and the project is explicitly removed.

## Table controls

The Mod Library, collection members, Virus Scanner, and ModMaker tables share row-selection controls:

- In the Mod Library, click a row or preview to open details without changing the checked mods. In the other tables, clicking a row selects it.
- Ctrl-click (Command-click on macOS) toggles an individual table row without clearing the others.
- Shift-click selects the inclusive range from the last selection anchor. Ctrl/Command+Shift-click adds a range to an existing selection. Ranges follow the current sort and filter, including across pages.
- Checkboxes toggle rows without modifier keys; Shift-clicking a checkbox adds a range.
- Up/Down, Home/End, and Page Up/Page Down move selection. Hold Shift to extend or shrink a range, or Ctrl/Command to move focus without changing selection. Space toggles the focused row.
- Ctrl/Command+A selects all matching, selectable rows across pages. Escape clears selection. The header checkbox selects or clears matching rows without changing selections outside the filter.
- Single-click a row or press Enter to open a library mod, collection member, or ModMaker workspace. Ctrl/Command-click, Shift-click, and checkboxes change selection without opening a mod. In the Virus Scanner, Enter toggles selection instead.
- Right-click a selected row to act on the selection, or an unselected row to target that row. Shift+F10 opens the focused row's menu where available.
- **Columns** opens a floating, viewport-constrained panel above the table. Toggle visibility or drag columns to reorder them; Escape, the close button, or an outside click dismisses the panel.
- Click a column heading to sort; click it again to reverse direction. Sorting applies to the visible filtered mods, and changing sort returns to the first page.

Unavailable scanner rows and busy tables cannot be selected. Embedded actions, such as a collection member's enable checkbox, do not change row selection.

## Tags

Use **+ New tag** in a mod's inspector to enter a name, choose a color and icon, and create the tag. A newly created tag is assigned to that mod.

**Gameplay**, **Graphics**, and **Trailer** are included among the default tags. Existing libraries receive them on their next launch without replacing same-named custom tags, their appearance, or their assignments. Deleting a default tag after this update keeps it deleted on later launches.

## Play a set of mods

Collections are the named sets; **Play** combines them into what launches.

- Tick collections to play just those, or tick **All mods** to play everything.
- With **All mods** ticked, click a collection to leave it out, and click it again to bring it back. "Everything except the Pixar Cars mods" is **All mods** plus one click.
- The launch bar shows the result, for example `540 included − 76 left out = 464 mods`. It is worked out when you press **Play**, so mods you add later are included unless they are in a collection you left out.
- **Save as new** keeps the combination as a profile you can switch back to.
- Play waits for startup discovery before restoring its selection. Deleted collections are named when known; notices can be dismissed. Old resolved missing-mod warnings are cleared instead of repeated at every launch. Generated game copies and deleted `db.json` entries are not treated as missing library mods.

## Archive deployment and storage

Your library archives are the only canonical copies. Play and collection folders deploy them directly; there is no intermediate ZIP cache.

- **Settings → Storage and paths** (also offered in the setup wizard) chooses how external mods reach the game:
  - **Automatic — recommended**: hardlink when the library and game share a volume, otherwise copy.
  - **Hardlinks only**: never copy; mods on another volume block launch with the reason shown.
  - **Copies — separate game files**: independent copies, reused on later launches instead of recopied.
- Repository mods already inside BeamNG's mod folder are used in place.
- Before anything is copied, Play shows the extra space needed and asks you to confirm. Insufficient space, changed archives, or unrecognized files in the managed folder block the launch with a specific reason.
- Deployment is journaled. If Studio or Windows stops mid-change, the next start restores the previous selection or completes the new one; your library archives are never touched.
- BeamNG must be closed while Studio changes the game's mods. Studio never closes it for you.
- Removing or replacing a mod also retires its generated game and collection-folder copies. A copy that might be the last surviving data is kept for review instead.

**Review storage** (in the same settings section) inventories library, game, collection-folder, and legacy-cache archives without changing anything. Hardlinked names are counted once. Only verified redundant copies are offered for removal, and each is checksum-verified against its library archive first. Archives with no surviving library copy can be recovered into the library. Exports, workspaces, backups, and unrecognized files are listed for context but never cleaned. Removing one name of a hardlinked file frees no space; results report what was actually removed.

---

## Technical details

The sections below are for contributors and people building from source. Regular users should use a packaged release rather than follow these steps.

- **Desktop:** Go and Wails v3; React/TypeScript frontend.
- **Storage:** SQLite metadata, content-addressed previews, isolated editing workspaces, immutable exports.
- **Repository layout:** `studio/BeamNGModStudio` contains the desktop app; `modkit` contains archive inspection/editing code and is referenced through a local Go module replacement.
- **Development status:** prerelease software. A successful build is not a substitute for testing first-run setup, mod import, Play, recovery, and AI behavior on a clean machine.

## Library storage and caching

Mod identities, archive locations, collection/tag memberships, ModMaker projects, and security analyses are separate related records. Full archive inspection documents remain in `artifacts`; they are not the library table's read model.

`artifact_summaries` is a small one-to-one projection used for library listing, filtering, counts, and sorting. SQLite triggers update it in the same transaction as archive metadata changes, and startup backfills existing archives without rescanning ZIPs. Listing responses omit archive inventories, variant/document contents, and detailed issue messages; `GetEntity` supplies complete inspection data when a mod is opened.

Full parsed manifests are cached on demand using artifact ID plus a unique metadata revision. Warm reads avoid fetching the large JSON column. Cached values are mutation-isolated; rollback cannot reuse an uncommitted revision. Retention is bounded by entry count and a 64 MiB source-JSON budget (not a fixed Go heap-size guarantee), and the cache is released when the store closes. Collection/tag changes remain ordinary relational updates rather than rebuilding archive metadata.

## Safety model

- Library archives are read in place and never unpacked during scanning.
- ModMaker extracts a separate workspace and records the source SHA-256.
- Editor and Virgil host tools can access only workspace-relative paths.
- Unsaved editor drafts persist in SQLite and are recovered after restart.
- Export validates the workspace, checks the source checksum before and after packing, writes atomically, and registers a new immutable ZIP.
- Test installs use the `modstudio-test-*.zip` namespace and verify their checksum before launch.
- Collections contain mods and other collections. Membership is many-to-many and cycles are rejected, so a mod or collection can be reused anywhere without being moved or copied.
- Every membership carries an enabled flag. A disabled mod keeps its place in the collection and stops shipping; a disabled child collection is not traversed from that parent. Enabled wins: a mod enabled in any reachable collection is included.
- Successful scans disable collection entries whose last indexed archive was removed. Startup also repairs stale entries left by older scans. Membership and ordering remain intact; restore the archive and re-enable the mod to include it again. Another active source keeps the mod enabled, and failed or cancelled scans do not disable it.
- On the first indexed library, the mods BeamNG already has enabled become one collection held by one profile, and that profile is selected. A fresh install therefore launches the mod set the user already had.
- Play resolves the selected profile's collections into one deduplicated mod set, deploys it into `current/mods/beamworlds-managed` under the chosen deployment mode, and updates BeamNG's native `db.json` active state atomically. Normal settings, controls, and saves remain shared.
- Profiles are named collection selections. Selecting, renaming, or deleting one never changes mod files or what BeamNG currently loads; only Play does.

## Build a fresh clone

### Windows development prerequisites

- [Go](https://go.dev/dl/) **1.25 or newer**; keep Go's automatic toolchain selection enabled if dependencies require a newer version.
- [Node.js](https://nodejs.org/) **20 or newer**, including npm.
- Git to clone the repository and use ModMaker source control.
- The Microsoft WebView2 Runtime.
- Wails CLI **`v3.0.0-beta.16`**, matching the version in `go.mod`. Do not substitute an unrelated `@latest` CLI.

Run these commands in PowerShell:

```powershell
git clone https://github.com/SignedAdam/beamworlds-mod-studio.git
cd beamworlds-mod-studio
go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-beta.16
# Ensure Go's bin directory is on PATH for this shell.
$env:PATH = "$(go env GOPATH)\bin;$env:PATH"
cd studio/BeamNGModStudio
wails3 build
```

The build task installs frontend dependencies, generates bindings, and stages a checksum-pinned **oh-my-pi 18.1.2** Windows runtime from its public upstream release before embedding it. Internet access is required for a clean build; end users do not need a separate Node.js or oh-my-pi installation.

The executable is written to `studio/BeamNGModStudio/bin/beamngmodstudio.exe`. Launch it directly, or use `Run BeamWorlds Mod Studio.cmd` at the repository root. Running a development build is not a clean-machine installation test.

### Checks and isolated testing

From the repository root:

```powershell
cd modkit
go test ./...
cd ../studio/BeamNGModStudio
go test ./...
cd frontend
npm ci
npm run build
```

For isolated manual testing, point `BEAMWORLDS_HOME` at a new test folder. This explicitly selected home is a strict configuration boundary: only its own `config.json` is read, never a parent checkout's configuration. Use a separate `dataDir`, mod library, and game-mod fixture; paths you enter in setup are still real filesystem locations, not automatically sandboxed.

### Packaging and publishing

Windows packaging requires [NSIS](https://nsis.sourceforge.io/) on `PATH` for an installer:

```powershell
# From studio/BeamNGModStudio, after building:
node build/collect-notices.mjs
powershell -File build/windows/package-release.ps1
wails3 task windows:package
```

The [Release workflow](.github/workflows/release.yml) builds an existing version tag, runs checks, and creates a **draft** release with a portable ZIP, installer, checksums, and a matching source reference. Review the exact artifacts on a clean Windows machine before publishing. Include the GPL license, runtime MIT license, and third-party notices; disclose unsigned status rather than claiming a signed build.

## License and BeamWorlds branding

BeamWorlds-authored code in this repository is licensed under **GNU GPL version 3 only** (`GPL-3.0-only`); see [LICENSE](LICENSE). Third-party components retain their own licenses and notices. The license is not a grant of rights to game files, third-party mods, or assets shown in screenshots.

The code license does not grant trademark rights to the **BeamWorlds** name or logo and does not imply endorsement of forks. You may accurately describe a fork's origin, but do not present an unofficial build as an official BeamWorlds release. No registered-trademark status is asserted here.

This repository's license does not automatically license separately developed BeamWorlds websites or services. Distribution of bundled third-party runtimes must satisfy their own terms as well as any applicable source-availability obligations.

BeamWorlds Mod Studio is an independent project, not an official BeamNG product.
