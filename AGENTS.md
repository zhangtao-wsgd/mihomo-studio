# Agent rules for Mihomo Studio

## Approval and scope

- Treat the approved V6 high-fidelity prototype and recorded product decisions as the current product baseline.
- Before a new feature or a material architecture change, present the plan and wait for explicit approval before editing implementation files.
- Work incrementally. Never remove a previously approved page, interaction, API, or behavior unless the user explicitly approves removal.
- Separate implemented behavior from prototype-only behavior. Do not label a mock, a persisted setting, or a controller reachability check as proof of end-to-end traffic behavior.

## Three principles

### Design is explanation

- Every control must make clear what it changes, what values mean, and what happens after saving or publishing.
- Explain risks at the decision point, not only in external documentation.
- Use business language first; expose Mihomo terms as precise supporting detail.

### Architecture is explanation

- Keep package/module responsibilities explicit and dependencies directional.
- Model desired state, immutable candidates, and deployment facts separately.
- Use stable IDs for references; never use display names as persistent foreign keys.
- Persist lifecycle transitions and audit outcomes. Unknown runtime state must remain unknown until explicitly verified.
- Keep upstream MetaCubeX Mihomo and metacubexd unmodified.

### Code is explanation

- Prefer domain-specific names, small functions, explicit state transitions, and simple control flow.
- Avoid magic strings when a named constant or typed value materially improves clarity.
- Comments should explain non-obvious constraints or rationale, not paraphrase code.
- Do not silently ignore persistence, validation, rollback, or audit errors.
- Validate external inputs at boundaries; enforce stable IDs and avoid unsafe path construction.

## Required workflow

- Analyze normal flow, failure/abort flow, ignored/false-positive flow, reverse/rejected/reactivation flow, and precondition guards.
- Before claiming a task complete, run formatting, unit tests, schema checks, and any available container/integration tests. Clearly state tests that could not run and why.
- Keep links in Markdown relative where applicable; do not commit machine-specific absolute paths.
- Update `docs/implementation-status.md` whenever implementation status changes.
