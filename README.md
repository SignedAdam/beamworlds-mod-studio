# BeamWorlds Mod Studio

Native Wails desktop application for indexing local BeamNG ZIP mods, grouping them into collections, editing isolated ModMaker workspaces, collaborating with Virgil, and launching an exact set of mods while preserving normal BeamNG settings, controls, and saves.

## Run

For this built checkout:

1. Double-click `Run BeamWorlds Mod Studio.cmd`.
2. On first launch, review the automatically detected BeamNG installation and user folder.
3. Choose the mod library and BeamWorlds staging location, then save the setup.

The CMD file is the only supported launcher. It starts `studio/BeamNGModStudio/bin/beamngmodstudio.exe`; the first-run wizard persists machine-local paths outside version control. The chosen Studio storage directory contains SQLite metadata, content-addressed images, editable workspaces, mod-profile cache, and immutable exports.

`config.example.json` remains available for portable or managed deployments. The executable is generated build output and is not committed. A fresh clone must be built once before the launcher can run it.

## Add existing mods

Choose **Add mod…** in the Mod Library to open the custom file explorer. Each opening starts in your Downloads folder; if Downloads is unavailable, the picker explains why and opens Home instead.

- Use Places, breadcrumbs, Back/Forward/Up, or paste a folder path to navigate. The eight most recently visited folders are remembered across restarts.
- Only folders and `.zip` files are shown. Filter the current folder or sort by name, size, or date modified.
- Select ZIPs using click, Ctrl/Command-click, Shift-click, or checkboxes. Keyboard arrows and Home/End navigate, Shift extends a range, Space toggles a file, and Ctrl/Command+A selects matching ZIPs. Double-click or Enter opens a folder.
- Choose **Add** to copy the selected ZIPs into your configured mod-library folder and index them immediately. Originals remain untouched. Different files with the same name receive numbered filenames; identical existing copies are reused.

Adding mods does not enable them in BeamNG or add them to a collection. If some files fail, successful imports stay in the library and the picker keeps the failed files selected with an explanation.

## Table controls

The Mod Library, collection members, Virus Scanner, and ModMaker tables share row-selection controls:

- Click a row to select it; Ctrl-click (Command-click on macOS) toggles an individual row without clearing the others.
- Shift-click selects the inclusive range from the last selection anchor. Ctrl/Command+Shift-click adds a range to an existing selection. Ranges follow the current sort and filter, including across pages.
- Checkboxes toggle rows without modifier keys; Shift-clicking a checkbox adds a range.
- Up/Down, Home/End, and Page Up/Page Down move selection. Hold Shift to extend or shrink a range, or Ctrl/Command to move focus without changing selection. Space toggles the focused row.
- Ctrl/Command+A selects all matching, selectable rows across pages. Escape clears selection. The header checkbox selects or clears matching rows without changing selections outside the filter.
- Single-click a row or press Enter to open a library mod, collection member, or ModMaker workspace. Ctrl/Command-click, Shift-click, and checkboxes change selection without opening a mod. In the Virus Scanner, Enter toggles selection instead.
- Right-click a selected row to act on the selected group, or an unselected row to target that row. Shift+F10 opens the focused row's menu where available.
- **Columns** opens a floating, viewport-constrained panel above the table. Toggle visibility or drag columns to reorder them; Escape, the close button, or an outside click dismisses the panel.

Unavailable scanner rows and busy tables cannot be selected. Embedded actions, such as a collection member's enable checkbox, do not change row selection.

## Tags

Use **+ New tag** in a mod's inspector to enter a name, choose a color and icon, and create the tag. A newly created tag is assigned to that mod.

**Gameplay**, **Graphics**, and **Trailer** are included among the default tags. Existing libraries receive them on their next launch without replacing same-named custom tags, their appearance, or their assignments. Deleting a default tag after this update keeps it deleted on later launches.

## Safety model

- Library archives are read in place and never unpacked during scanning.
- ModMaker extracts a separate workspace and records the source SHA-256.
- Editor and Virgil host tools can access only workspace-relative paths.
- Unsaved editor drafts persist in SQLite and are recovered after restart.
- Export validates the workspace, checks the source checksum before and after packing, writes atomically, and registers a new immutable ZIP.
- Test installs use the `modstudio-test-*.zip` namespace and verify their checksum before launch.
- Collections group mods and other collections. Membership is many-to-many and cycles are rejected, so a mod or collection can be reused anywhere without being moved or copied.
- Every membership carries an enabled flag. A disabled mod keeps its place in the collection and stops shipping; a disabled child collection is not traversed from that parent. Enabled wins: a mod enabled in any reachable collection is included.
- On the first indexed library, the mods BeamNG already has enabled become one collection held by one profile, and that profile is selected. A fresh install therefore launches the mod set the user already had.
- Play resolves the selected profile's collections into one deduplicated mod set. External archives are hardlinked or copied into `current/mods/beamworlds-managed`, BeamNG's native `db.json` active state is backed up and updated atomically, and normal settings, controls, and saves remain shared.
- Profiles are named collection selections. Selecting, renaming, or deleting one never changes mod files or what BeamNG currently loads; only Play does.

## Build a fresh clone

Install Go, Node.js, and Wails v3, then run from the repository root:

```text
cd studio/BeamNGModStudio/frontend
npm ci
cd ..
wails3 build
```

The production executable is written to `studio/BeamNGModStudio/bin/beamngmodstudio.exe`.
