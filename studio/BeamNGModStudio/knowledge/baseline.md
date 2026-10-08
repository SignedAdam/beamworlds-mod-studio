# BeamNG mod architecture baseline

## Non-negotiable archive model

A distributable mod is a ZIP whose logical content paths start at BeamNG-recognized roots. Common roots are `vehicles/`, `levels/`, `lua/`, `ui/`, `art/`, `scripts/`, `settings/`, `gameplay/`, `campaigns/`, `scenarios/`, `flowEditor/`, and `mod_info/`. A single unknown outer folder may be a packaging wrapper; reason about the unwrapped logical paths, but preserve the original physical layout unless the requested change is a packaging repair.

Treat the selected source ZIP as immutable. Work only in the provided editable workspace. Never edit another mod, the base game, the ModLibrary source, or generated cache files. Export a new ZIP after validation.

## Syntax and path rules

- BeamNG JSON-family files frequently use JSON5 features: comments, trailing commas, unquoted keys, or a UTF-8 BOM. Do not force strict-JSON assumptions on `.jbeam`, `.pc`, or game metadata.
- Normalize archive separators to `/`. Reject absolute paths, drive prefixes, `..` traversal, NUL bytes, encrypted entries, duplicate case-insensitive paths, and unsupported compression.
- Preserve filename case and established namespaces. BeamNG references can be case-sensitive even on Windows once packed or shared.
- Keep files under the mod's existing namespace. Avoid generic new names that can collide with another mod.
- Do not rewrite unrelated large JSON5/JBeam documents just to change formatting. Narrow edits make review and merge behavior safer.

## Dependency and identity model

A logical mod, an immutable analyzed artifact, and an archive location are separate identities. Paths can disappear, ZIP bytes can change in place, and identical artifacts can be linked from more than one location. Do not infer authorship or compatibility from a filename alone.

References to base-game slots, materials, meshes, textures, extensions, UI events, or level assets are dependencies. Report unresolved references; do not copy base-game or third-party assets into the workspace as a shortcut. A reference may intentionally target content supplied by BeamNG or a declared companion mod.

## Editing workflow

For reported in-game problems, begin with `game_log` and the surrounding stack trace. Save its cursor before a live reproduction and read again afterward; do not attribute unrelated mods' errors to the selected mod.
Use `game_source` to check uncertain API signatures and lifecycle behavior against the installed game's Lua. It is read-only; keep repairs in the workspace.

1. Inspect the manifest, issues, logical roots, namespaces, and metadata.
2. Select the category-specific context for the mod's actual content, including every relevant context for a mixed mod.
3. Search before changing names or references. Map producers to consumers across files.
4. Make the smallest coherent source change. Update every in-workspace caller in the same change.
5. Run workspace validation and inspect the source diff.
6. Fix errors before export. Treat warnings as explicit review items, not automatic failures.
7. For live testing, use `mod_game_test` to export and run the build in an isolated BeamNG session without interrupting the player's game.
8. Inspect its fresh log and level, vehicle, and simulation observations. Fix confirmed failures and retest changed builds. Report the scenario actually exercised. The user's library mod already holds your saved changes, so do not hand them ZIPs to install.

## Validation expectations

A valid archive is not necessarily a working mod. Structural validation should cover safe paths, readable JSON5, recognized roots, duplicate paths, matching vehicle configuration metadata, and category-specific requirements. Runtime validation catches dynamic references, Lua behavior, UI bridges, materials, and engine-version compatibility.

Never silence an error by deleting the failing feature, swallowing exceptions, inventing assets, or adding a fake fallback unless the user explicitly asks for that behavior. Fix the source contract and preserve the original mod's intent.
