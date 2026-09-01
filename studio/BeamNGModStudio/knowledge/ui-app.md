# UI and in-game app context

## Multi-layer app model

A classic BeamNG UI app commonly uses `ui/modules/apps/<AppName>/` with:

- `app.json`: app name, author, version, description, directive/component identifier, DOM element, category types, and default dimensions/position.
- `app.html`: the view/template.
- `app.js`: app registration, controller/directive/component logic, event subscriptions, and bridge calls.
- optional CSS, images, localization, and shared UI modules.

The UI is only one layer. Gameplay-backed apps usually bridge to a game-engine Lua extension under `lua/ge/extensions/...`, which may then communicate with vehicle Lua or controllers. Settings defaults may also live under `settings/` or scripts. Trace the full chain:

`app.json identifier -> JavaScript registration -> template bindings -> UI-to-Lua bridge -> GE extension API -> vehicle/gameplay state -> engine-to-UI event -> scope/component update`.

Names and payload shapes are contracts. Changing one producer requires updating every consumer in the workspace.

## Legacy and current APIs

Existing mods may use the legacy AngularJS `beamng.apps` module, directives, `$scope` events, and `bngApi.engineLua`. Preserve the established framework for a narrow repair; do not mix a second framework into one app. Newer BeamNG UI systems may use different modules and component APIs. Infer the framework from the existing app and nearby base-game conventions supplied to the workspace, not from assumptions.

When constructing Lua calls from JavaScript, serialize data rather than concatenating untrusted text into executable Lua. Existing quoted bridge calls deserve careful review for escaping. Keep event payloads small and versionable.

## Lifecycle and state

- Initialize defaults coherently with the engine-side implementation. One authoritative source is preferable when the app can request current configuration.
- Apply engine state received from events asynchronously using the framework's supported update mechanism.
- Clean up timers, DOM listeners, streams, and subscriptions when the app is destroyed.
- Stop hold-to-actuate behaviors on mouse-up, focus loss, destruction, and error paths so an action cannot remain latched.
- Treat browser-local state as UI preference only, not authoritative gameplay state.
- Disabled or server-locked settings must also be enforced engine-side; disabling a control alone is not a security or consistency boundary.

## Manifest and presentation checks

The `directive` or component identifier must match JavaScript registration. `domElement` must instantiate that identifier correctly. Template paths are archive-root URLs and must match member case. Validate dimensions, overflow, keyboard/focus behavior, and readable states at BeamNG's supported UI scaling.

Do not claim UI correctness from source inspection alone. Load the app in BeamNG, add it to a layout, exercise controls, close/reopen it, switch vehicles or levels when relevant, and inspect the UI console/runtime log.

## Runtime review

Look for app registration failures, missing templates/assets, JavaScript exceptions, digest/update errors, failed Lua bridge calls, absent engine extensions, payload mismatches, leaked timers/listeners, and repeated event storms. Confirm that settings round-trip: UI change reaches Lua, authoritative state returns, and the displayed value matches actual behavior.
