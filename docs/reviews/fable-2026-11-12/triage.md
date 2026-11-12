# Independent architecture and coverage review, 2026-11-12

This is the first-review historical triage. Its findings drove the subsequent remediation; current closure and gate evidence belongs in `docs/VERIFICATION.md`. The archived overlay uses the superseded AfterCommit API and is executable only against the pre-remediation source, not the current Then contract.

## Conclusion

The generic Go framework has substantial implemented components, and the recorded local gates passed. That is not sufficient evidence that the complete Toolbox service-authoring and interaction toolset is preserved. The earlier unqualified completion statement is withdrawn pending composition and contract work. Existing storage/Dia verification remains valid for what those tests exercise.

Claude Code ran the explicitly requested `claude-fable-5-2`; result metadata confirms that model, success,94 turns and373125ms. It had only Read/Glob/Grep tools, no MCP servers and no session persistence. The user explicitly authorized disclosure of the relevant source/tests to Anthropic after automatic review initially rejected it. See [original report](claude-report.txt) and [model provenance](provenance.json). Claude ran no tests. The parent separately inspected the findings and ran the two probes below.

## Validated findings

| Finding | Evidence and qualification | Required follow-up |
| --- | --- | --- |
| Generic composition lacks a complete working demonstration | `cmd/dia/main.go:182,202` consumes the specific SessionProjection and Dia API. Generic Framework/Procedure/Engine are separate libraries; the existing full native flow does not exercise them together. This is a composition/acceptance gap, not proof that the libraries cannot be composed. | A runnable reference service plus native command -> append -> outbox -> generic projection -> protected view flow, exercising actual production adapters. |
| Missing queue/replay adapters and nested command client | `command.Queue`, `command.Consume` and `projection.Producer` have no production worker wiring; a typed nested command client with deliberate child identity/trace propagation is absent. Async commands fail explicitly when no queue is supplied. | Bind supported task/messaging adapters to these consumer interfaces, provide authenticated delivery routes and test duplicate/uncertain delivery plus two-service composition. |
| Refreshed claims change retry identity | `command/command.go` copies authenticated claims into Transaction; `eventstore/store.go:150` fingerprints the whole normalized Input. Probe confirms a same-ID/payload/context request with changed exp/jti returns ErrIdempotency. Framework replaces claims from the current authenticated identity, so this matters for remote retries. | Separate stable request identity from volatile authentication evidence without losing original committed audit bytes or admitting changed principals/payloads. Specify and test the contract before changing hashing. |
| Callback-only response is not recoverable on duplicate | Probe confirms a committed response body is absent on duplicate. Current AfterCommit is explicitly fresh-only, unlike JS thenFn(all outcomes). The current session also described fresh-only behavior, so this is a confirmed semantic divergence with a response-contract decision, not grounds to silently revert it. | Separate deterministic/recoverable response construction from fresh-only best-effort side effects, or explicitly select an all-outcomes callback contract and update consumers/tests together. |
| KMS signing is not composed into Dia JWT | `dia/auth.go:36` requires a concrete local ECDSA private key. Provider SignES256 methods exist but cannot be injected into that API. | Consumer-owned context-aware signer seam, local and provider adapters, runtime selection and signing/verification fixtures. |
| View query/group expressiveness is narrower | `view.Query` has equality conjunction, offset/limit and ID order; `Scope.Owner` has conjunction only, no any-of principal groups. Text/range/custom-sort/bootstrap equivalents are absent. | Map each retained JS query/auth capability to typed behavior or an explicit approved scope decision; test group-union authority independently of caller filters. |
| Jobs do not carry claims | `job.Input` has Payload/Context only; Framework job route injects context, not claims. | Explicit trusted job identity/claims propagation and boundary test. |

## Findings qualified or not established

- A library does not need to be imported by every application to be implemented. Dependency closures are idiomatic Go; missing JavaScript-style injected function names alone are not a capability defect.
- Remote GCP/AWS outbox publication and consumption are real implementations in `runtimepub`, using official SDKs, and are wired into cmd/dia. The absence of imports from the separate cloud packages does not imply that remote publication is missing. Generic command queue and projection replay wiring are the narrower confirmed gaps.
- ID-addressed mutations and independently fenced single-source rows are documented redesigns. Named array filters exist in `view/mutation.go`; they are not missing. Query-addressed multi-row selection still needs a clear application composition contract.
- Shared-backend row IDs have no view-store identity. Source checks reject foreign roots/owners before the version fence, so this is not a general cross-tenant overwrite. Two logical views with the same source/owner/root/ID would alias if deliberately assigned one backend. The generic gateway allows that registration. Native reproduction and an explicit store-namespace rule remain outstanding.
- Bearer-only authentication, no cookie middleware and no multipart gateway are currently documented/narrower surfaces. Whether all historical modes must be restored needs an explicit retained-capability decision, not an automatic claim that every historical middleware must return.
- No claim is made that storage, wallet/EVM or all security paths received a complete independent audit. Claude explicitly excluded deep review of those areas.

## Reproduction evidence

[Two review probes](command_review_test.go.txt) use the existing command test fixture and real Store.Append. They are injected using a Go overlay, without modifying implementation or normal test files. [Output](reproduction.log) records both failing proposed acceptance expectations under Go1.27.4 with the race detector:

1. Same request after refreshed exp/jti -> `idempotency key reused with different content`.
2. Callback response retry -> `first=1 retry=null`.

The probe command exited1 as expected for those unmet expectations; it is not counted as a passing gate. No native database or external provider was used. The existing753/927 passing totals are historical baseline evidence, not coverage of these new expectations.

Historical reproduction command for the pre-remediation module tree (not the current API):

```sh
python3 - <<'PYCODE'
import json
from pathlib import Path
root = Path.cwd()
replacement = root / 'docs/reviews/fable-2026-11-12/command_review_test.go.txt'
Path('/tmp/blossm-review-overlay.json').write_text(json.dumps({'Replace': {
    str(root / 'command/claude_review_test.go'): str(replacement)
}}))
PYCODE
GOTOOLCHAIN=go1.27.4 go test -mod=readonly -race \
  -overlay /tmp/blossm-review-overlay.json ./command -run '^TestReview' -count=1 -v
```

## Coverage conclusion

The baseline unit logs contain236 top-level tests and753 test/subtest pass results. Native mode contains298 top-level tests and927 results, including the unit suite. These cannot be equated to1688 JS Mocha cases,15 tooling tests and26 additional native Mongo cases. The inventory links packages to tests but is not a per-behavior equivalence matrix. The strongest current evidence covers native persistence, replay, idempotency and the concrete Dia flows. Complete generic framework composition and several retained JS behavior families need further implementation and acceptance tests before restoring a whole-scope completion claim.
