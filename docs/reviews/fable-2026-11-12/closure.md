# Review remediation evidence

The original completion claim was reopened after independent review. Reports and
model provenance remain in this directory, including findings that were later
fixed. Claude reviews are read-only source evidence, not test execution or a full
security audit. Current execution results belong in [VERIFICATION](../../VERIFICATION.md).

| Finding | Owning change and executable evidence |
| --- | --- |
| Generic components lacked executable composition | `cmd/framework` + `serviceapp`; `integration.TestGenericFrameworkNativeFlow`, `TestGenericAsyncDeliveryAndReplayNative`, `TestContainerGenericFrameworkLifecycle` |
| Token refresh changed duplicate identity | `eventstore/fingerprint.go`; `TestRequestFingerprintSeparatesTokenLifetimeFromAuthority`, `TestEventHashStillBindsVolatileClaims`, real native HTTP refresh retry |
| Callback response absent on duplicate | `command.Result.Then` all ordered outcomes, detached metadata; command outcome/mutation tests and native duplicate response assertion |
| Cross-actor command ID collision | Actor-bound generated keys, root excluded; `command.TestActorNamespacePreservesNestedChildIDs` plus principal/claim refresh and retarget tests |
| Nested RPC lacked real credentials and handler composition | `CommandClient`, delegated signer/verifier/transaction check, runtime peers and actual `copy` Main; HTTP/native lineage, revoked session, wrong audience and non-command rejection assertions |
| Nested retry reread mutable source | Explicit copy sourceVersion + verified History; native source mutation between first child commitment and identical parent retry |
| Jobs lacked trusted claims | `job.Identity` + Framework injection; forged body tests and native subject/principal comparison |
| KMS signer was not composed | Context-aware Dia signer, pinned verification and `runtimeauth.Open`; local/GCP/AWS SDK signing fixtures and failure tests |
| View capability/namespace gaps | Typed range/text/sort/group scopes, named physical IDs, query-selected atomic mutations; native namespace, group, rollback and paging contracts |
| Empty group union failed open | `RequireOwnerAny`; `TestRequiredOwnerAnyEmptyDeniesEveryOperation` and both-database native cases |
| Query writes could suppress later projections | Locked source frontier validation; `TestNativeViewQueryWriteCannotPreemptProjection` uses actual Append/Engine.Play sequence |
| Queue/replay/publication composition absent | Tasks/SQS adapters, authenticated admitted consumers, verified publication triggers and lifecycle workers; provider SDK seams and native outbox flow |
| Poison deliveries blocked subsequent work | Atomic minimal rejection journal before acknowledgment; native changed-retry rejection followed by valid command, unknown-commit/cancellation recovery |
| Replay runs were suppressed by static dedup IDs | Required typed RunID through CLI/HTTP/scheduler/delivery; same run dedups, new run schedules new work, echoed progress identity checks |
| Store peers could authorize delivery | Separate explicit delivery verifier; native store-peer rejection and provider identity fixtures |
| Rejections were operationally invisible | Acceptance.EnvelopeID, authorized lookup, fresh confirmed best-effort warning; concurrent/observer-error/unknown-commit tests and native submitted ID -> journal HTTP lookup |
| Internal service credentials had no issuer | `blossm internal-token` reuses runtimeauth, bounded lifetime/configured subject and audience; actual CLI mint -> plain loopback HTTP serviceapp replay plus wrong subject/audience and KMS failure tests |
| HTTP204 wrote an invalid body | Shared header-only response; actual HTTP empty-body/no-error regression |
| Cloud Run invocation contract was implicit | Required invocation configuration, exact generated annotation and review prerequisites; framework IAM conservatively rejected as unverified intact-token forwarding, application mode supported, Dia IAM requires an edge; official provider documentation linked in deploy guide |

The user explicitly chose one signing trust domain per network. Subject/audience
checks are not compromise isolation among services sharing signing authority.
The architecture documents this boundary. Streams retain a bounded deadline;
malformed envelopes require provider redrive because they cannot establish a
trusted journal identity. These limits are not hidden success paths.

The package/family ledger maps retained scope and assertion locations. It does
not claim one-to-one equivalence between Go test/subtest totals and JavaScript
Mocha cases. Live provider, deployment and chain behavior remain unverified.


The [fifth focused Fable review](claude-remediation-5.txt) found no blocker in the
remaining deltas and confirmed the conservative IAM guard, loopback URL checks,
CLI-minted replay, redrive prerequisite, JSON rejection logger and native HTTP
observer assertions. It did not execute tests or audit unchanged code. Its root
plan/closure wording observations have been reconciled; source/test gates remain
separate evidence.
