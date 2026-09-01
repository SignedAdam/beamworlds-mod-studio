# Level and map context

## Layout

A map normally lives under `levels/<level-id>/`. The folder ID is a runtime namespace, not just a display name. Renaming it requires a complete migration of paths and references.

Common layers:

- `levels/<id>/info.json`: title, description, previews, size, biome, roads, features, suitability, authorship, default spawn, and spawn point metadata.
- `main/items.level.json` and nested `main/MissionGroup/**/items.level.json`: serialized scene/object groups.
- terrain files such as `.ter` plus terrain-related materials and layer maps.
- forest data and brush/item definitions.
- `art/`: meshes, collision assets, textures, decals, roads, signs, buildings, and environment resources.
- `*.materials.json`: material names and texture-channel paths.
- `facilities/*.json`: garages, dealerships, delivery sites, or other gameplay locations.
- missions, flowgraphs, scenarios, or Lua extensions when the level includes gameplay.

Large maps can contain thousands of entries and very large texture assets. Make targeted edits; do not deserialize and rewrite the whole level for a narrow change.

## Reference graph

Scene objects reference datablocks, mesh paths, materials, terrain, forests, decals, and transforms. Materials reference texture paths and shaders. `info.json` spawn IDs must agree with actual scene spawn objects. Facility and mission data may refer to level IDs and named objects.

Before renaming or moving an asset, search all scene, material, facility, mission, and script documents. Case and slash differences can break an archive even when loose Windows files appeared to work.

Some references intentionally target base-game assets. Do not copy those assets into the mod. Mark an unresolved local reference as a possible external dependency unless runtime diagnostics prove it missing.

## Safe changes

- Metadata-only changes: preserve the level ID and preview paths; validate referenced preview members.
- Spawn changes: update metadata and the actual scene object as one contract; check rotation and terrain placement in game.
- Material changes: preserve material identifiers used by scene objects; confirm every texture channel exists or is an intentional external reference.
- Object additions: place them in an appropriate MissionGroup subtree and use unique persistent IDs where the format requires them.
- Terrain and forest changes: avoid binary edits through a text tool. Binary assets require the correct BeamNG editor or an explicit supplied replacement.
- Performance changes: evaluate mesh density, collision complexity, vegetation, texture size, and object count; deleting content blindly is not optimization.

## Packaging checks

The logical ZIP root must expose `levels/<id>/`, not an extra release folder. Ensure required terrain, material, model, texture, forest, and facility files remain included. Detect duplicate case-insensitive assets and unsupported compression. Preserve large unchanged entries byte-for-byte when exporting when possible.

## Runtime review

After a test launch, inspect logs for level load failures, missing scene or terrain files, material/texture lookup errors, missing datablocks, invalid persistent IDs, failed forest data, facility/mission errors, and Lua exceptions. A successful main-menu launch is insufficient; load the edited level and exercise the changed location or gameplay path.
