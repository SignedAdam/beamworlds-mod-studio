# Vehicle and JBeam context

## Layout and linkage

Vehicle content normally lives under `vehicles/<namespace>/`. Keep the namespace stable unless performing a complete, reviewed migration.

Typical layers:

- `info.json`: vehicle-level presentation and aggregate metadata.
- `info_<configuration>.json`: one configuration's title, description, performance, physical, career/value, years, paint, and classification fields.
- `<configuration>.pc`: selected `parts`, tunable `vars`, and configuration format version.
- `*.jbeam`: part definitions and physical/functional data.
- meshes and materials: flexbody meshes, props, textures, and material definitions.
- `lua/controller/*.lua`: vehicle controllers loaded from JBeam controller tables.
- `input_actions*.json` and `inputmaps/*.json`: player actions and bindings.
- preview images: usually basename-matched to the `.pc` configuration.

A configuration is a reference graph. `.pc.parts[slot]` selects a JBeam top-level part name. That part's `slotType` must satisfy the parent slot contract. Child `slots` expose further slot keys. Empty selections can be intentional. Do not rename a part, slot, action, controller, mesh, material, or variable without updating every in-workspace reference.

## JBeam semantics

A JBeam file is JSON5-like but its table sections have BeamNG-specific semantics. Preserve header rows and inline option objects.

Common sections and checks:

- `information`: human metadata and value.
- `slotType`: the part's insertion contract.
- `slots`: table header followed by slot declarations; defaults must resolve where the dependency is local.
- `nodes`: header such as ID and coordinates, interleaved option rows, then node rows. IDs must be unique within their effective part and referenced nodes must exist after part assembly.
- `beams`, `triangles`, `hydros`: table headers plus rows that reference node IDs. Option rows change defaults for following rows.
- `flexbodies`: mesh name and node-group bindings. Mesh and group references are separate contracts.
- `props`: visual or animated object bindings.
- `controller`: loader table. Preserve its BeamNG table shape; a controller filename usually resolves beneath the vehicle controller Lua path, and a configured name is what other code retrieves.
- action-enabling tables: action IDs must match input-action definitions.

Do not treat every array as homogeneous data. Header rows and option-object rows are meaningful. Comments and trailing commas may be intentional and valid.

## Physics changes

For nodes and beams, reason about assembled topology, symmetry, mass, collision flags, break groups, deformation/strength, bounded-beam limits, damping, and precompression together. A syntactically valid row can still create instability, invisible geometry, inverted response, or an unbreakable structure.

Hydraulic or active suspension spans several layers: JBeam hydros expose named actuators and input sources; a vehicle controller computes those inputs; actions call controller behavior; an input map binds controls. Tuning one layer without tracing the others can leave a working UI with no physics response, or a controller writing an input source that no hydro consumes.

Prefer symmetric edits when the existing design is symmetric. Preserve left/right suffix conventions. When intentionally asymmetric, explain it in the change review.

## Configuration edits

A `.pc` clone requires a unique safe basename, cloned metadata where present, and a matching preview when available. Change the display name in metadata rather than the source filename alone. Validate every selected part against local JBeam definitions or explicitly acknowledge its base-game/companion dependency.

Performance and descriptive metadata should reflect real behavior; do not fabricate values. Career price/value fields affect gameplay and should be changed only when requested.

## Runtime review

After test launch, look for JBeam parser errors, duplicate parts, missing slots, missing nodes, flexbody or mesh failures, material warnings, controller load errors, Lua stack traces, unknown input actions, and repeated instability messages. Structural counts are useful anomaly signals, not correctness proof.
