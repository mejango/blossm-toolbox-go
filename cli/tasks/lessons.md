# CLI lessons

## 2026-11-12: Separate selected toolchain from tested compiler

Keep official security pins even when the installed compiler is older. Record
the dated official source and actual compiler beside test evidence; explicitly
include toolchain downloads in pending dependency approval and rerun selected
toolchain gates before claiming release verification.

## 2026-11-12: Reconcile deliberate replacements instead of retaining false gaps

When the parent explicitly selects a fresh authentication or trust-discovery
contract, record that decision with its actual owning source and tests. Do not
keep obsolete header families or automatic public-IP discovery as implementation
gaps, and do not turn source presence or JavaScript vectors into passing Go SDK
verification. Recheck files being produced concurrently before final handoff.

## 2026-11-12: CI belongs to the actual Git root

A module directory is not automatically a Git repository. Place active workflows
under the actual repository root and set working-directory/cache paths for nested
modules. Do not count an undiscovered workflow template as an activated gate.

## 2026-11-12: Reconcile generated adapters when core owners arrive

Templates created before their core owners can retain parallel validation and
execution rules after the real implementation lands. Generate typed constructors
or delegation to the owner, and compile plus exercise those owner contracts in
the disposable scaffold fixture. Do not keep a second Job/Projection/Compose loop.

## 2026-11-12: Replay identity belongs to one logical run

When the owner changes replay identity to an explicit request contract, remove
static process configuration and per-message defaults together. CLI validation
and response checks must reuse owning RunID/ProjectionClient APIs. Preserve one
ID across pages and uncertain retries, and reject obsolete settings instead of
silently applying them. Regression: cli/replay_test.go.
