# Script and mixed-mod context

## Script layers

BeamNG Lua can run in distinct contexts with different APIs and lifecycles:

- game-engine extensions under `lua/ge/extensions/` manage world, UI, gameplay, and cross-vehicle coordination;
- vehicle extensions/controllers under vehicle namespaces or vehicle Lua paths run per vehicle and interact with electrics, powertrain, sensors, or JBeam inputs;
- gameplay, scenario, flowgraph, and editor scripts have their own lifecycle hooks;
- startup or utility scripts may be loaded from `scripts/` or referenced by another layer.

Do not call an API merely because it exists in another Lua context. Follow the existing extension location, hook names, registration mechanism, and message bridge.

## Extension contracts

Map every exported function and hook to callers. Typical contracts include extension load/unload, mission or vehicle lifecycle hooks, update ticks, serialization, UI callbacks, and event emission. Preserve externally referenced extension names and function names unless all callers are migrated.

Avoid global state collisions. Keep state inside the extension table/module pattern already used by the mod. Reset state at the correct lifecycle boundary. A reload path should not double-register callbacks, timers, or handlers.

For high-frequency hooks, avoid per-frame allocations, repeated path searches, excessive UI messages, and unbounded tables. Gate expensive work by state and cadence. Keep diagnostics useful but do not log every frame.

## Settings and persistence

Settings can span shipped defaults, user preferences, server policy, and runtime state. Establish precedence explicitly. Validate numbers, enums, and optional fields at the engine boundary. Never trust the UI alone to enforce a server lock or gameplay invariant.

When saving user data, write only to BeamNG-supported user locations and formats. Do not write back into the ZIP or ModLibrary source. Keep migrations for renamed keys when the mod already maintains persistent state; otherwise prefer a clean cutover inside the editable workspace.

## Mixed mods

A mixed archive may combine `art/`, `lua/`, `scripts/`, `settings/`, `ui/`, vehicle content, or level content. Apply every relevant category context. The central risk is a cross-layer contract that looks correct in isolation:

- UI event names and payload keys must match Lua emitters.
- engine extension names must match UI bridge and other Lua callers.
- vehicle controller inputs must match JBeam actuators and input actions.
- art/material paths must match script-spawned assets.
- settings defaults and runtime defaults must agree.

Search the entire workspace before changing any shared identifier.

## Runtime and compatibility review

Inspect fresh logs for Lua syntax errors, stack traces, missing modules, nil API calls, extension load order failures, hook errors, malformed settings, network/BeamMP policy conflicts, and UI bridge failures. Exercise reload/reset/recover flows as well as the happy path. A script that works once but leaks state across vehicle replacement or level reload is not complete.

Do not hide a failing hook with a blanket protected call. Handle only expected recoverable errors and preserve actionable diagnostics for programming faults.
