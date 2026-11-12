# Dia port lessons

- Store Aggregate.Version is the last event index, not count: absent is -1. Keep the conversion to producer Expected count at one private adapter and exercise real stores.
- Session creation request identity must not become an unbounded account credential. Use random creation secrets, prohibit upgraded-state recovery from anonymous creation, and check token identity against current state.
- Lost-response recovery must identify the exact prior transition. Never refresh arbitrary current context from an obsolete access token.
- Verify canonical representation through actual adapters. Integer timestamp JSON initially used exponent notation; the shared canonical encoder owns the decimal-safe integer fix, not domain-specific numeric parsers.
- Reserve proof attempts before comparing codes. Bind reuse to the same guess, bound consumed-proof recovery too, and test alternate guesses under one key.
- Permission inspection followed by a write on another root is not an atomic authorization invariant. Dynamic role grants share the role revision with permission expansion; test both commit orders.
- Keep exact semantic receipt checks when resolving concurrent retries. A same-key timestamp race may recover only a matching committed receipt; a different winning challenge request conflicts.
- Query supports scalar predicates. Discover candidate groups with reference components and post-filter the complete reference to avoid mixed-array-element matches.

## 2026-11-12: Verify durable application methods at the actual HTTP boundary

The comms routes called direct adapters and discarded the request key despite durable command methods existing. Route commands through their durable owner and preserve explicitly public challenge/reference recovery data alongside truthful error status. A returned reference is not evidence of commitment or delivery. Native HTTP recovery checks must cover both stores, not only service-method fixtures.

## 2026-11-12: Bind state-derived commands to original request semantics

Subject-based role removal expands mutable state, so the derived event payload cannot identify an identical retry. Persist the caller/session, target, explicit roles and subjects with the exact prepared effects in the same atomic receipt. Authenticate before recovery; acknowledge that receipt without replay, regrant removal, or granting authority to a new request. Native concurrent/self-removal/regrant regressions own this rule.

## 2026-11-12: Reconcile committed effects independently of backend error labels

Any append error may race with an identical committed attempt. Read the exact operation receipt once: matching digest proves success, different digest proves key reuse, and absence/read failure preserves the original append error. Do not detach canceled contexts, replay actions, or infer commitment from a reference. A provider must remain uncalled when preparation cannot be proven; focused in-memory error-boundary tests enforce this.

## 2026-11-12: Compose signing adapters at the actual token consumer

Existing provider signing methods do not establish usable KMS-backed JWT issuance when JWT requires a local private key. Own a contextual signing interface at the JWT consumer, pass the request context through every service caller, and verify the returned signature against pinned public trust. Share runtime provider selection across binaries. Real provider-adapter fixtures and runtime-selection checks prove composition without claiming live IAM/KMS validation.
