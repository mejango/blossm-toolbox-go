# RPC implementation

## Plan refinement
- **Objective:** Implement only rpc with the exact Options/New/Do/Open API, bounded streaming and real offline HTTP checks; no module edits.
- **System fit:** Explicit caller URL and headers feed one transport owner consumed by fact; Open validates HTTP status before exposing a stream. No discovery, delivery retry or deployment authority is added. Open explicitly attaches its derived request context to response.Request so fact callbacks observe deadlines without reading.
- **Reuse and simplicity:** Reuse net/http, context, url and encoding/json; Do consumes Open so deadline, status and limit rules have one owner. Reference generic RPC uses GET/DELETE queries and JSON writes.
- **Evidence and unknowns:** Read root AGENTS.md, workflow/ponytail/SKILL.md, docs/PLAN_REFINEMENT.md, tasks/lessons.md and reference generic/request source. No descendant instructions exist. Supplied custom RoundTrippers remain caller-owned behavior.
- **Verification:** Offline go test -race and go vet for ./rpc with real httptest cover bodies/query/auth, exact byte boundaries, total deadlines/Close, typed errors, redirect refusal, traversal/config rejection and disconnected writes without replay.
- **Resource budget:** One implementation file plus focused tests/docs; default 30 seconds and 8 MiB, no dependency downloads. Replan if the existing module prevents offline execution.

## Work
- [x] Read references and refine scoped contract.
- [x] Pass root plan gate before code.
- [x] Implement shared Open transport and Do buffering, document exact API.
- [x] Verify real HTTP failure boundaries and record results.

## Review

Implemented client.go, client_test.go and README.md within rpc only. No go.mod
edits, external imports or dependency downloads. Final offline `go test -race
-count=1 ./rpc` and `go vet ./rpc` pass on installed Go 1.27.1. Real loopback
listeners and the existing Go cache required sandbox escalation. The callback
case observes the Open-derived deadline through response.Request.Context()
without an active body read; Close immediately cancels the same context.
The HTTP tests also cover query/JSON/auth, bounded partial reads, redirects,
invalid inputs, HTTP errors and disconnected side-effect calls. Custom
RoundTripper retry behavior remains caller-owned, as documented.

## Plan refinement
- **Objective:** Fix clean EOF cancellation only in rpc so fact can finish buffered-record callbacks before explicit Body.Close; retain deadline/limit/error behavior.
- **System fit:** Open owns the callback context; network EOF is not consumer completion. The caller closes after decoder callbacks finish, while the existing total deadline remains active.
- **Reuse and simplicity:** Keep boundedBody as the sole owner; skip automatic Close for io.EOF in both read paths, without a new lifecycle abstraction.
- **Evidence and unknowns:** Integration review identified cancellation on ordinary and probe EOF; existing tests did not exercise buffered records after transport EOF.
- **Verification:** Add real httptest with buffered JSON records after forced EOF for exact-limit and below-limit bodies; assert live callback context, explicit Close cancellation, and deadline expiry after EOF. Run offline race tests and vet.
- **Resource budget:** Small owner fix with one regression family, scoped docs and lesson; no dependencies, module edits or changes outside rpc.

## EOF correction
- [x] Check scoped refinement, add failing buffered-record regression.
- [x] Preserve context on both clean EOF paths and update docs/lesson.
- [x] Run package race tests and vet, record results.

### EOF correction review

The real httptest regression failed before the fix in all four cases: ordinary
EOF and exact-limit probe EOF, each with explicit Close and deadline completion.
Both read paths now preserve context on clean EOF; non-EOF errors still cancel.
Buffered JSON callbacks finish with a live context; explicit Close cancels and
the original timeout still expires after EOF. Final offline `go test -race
-count=1 ./rpc` and `go vet ./rpc` pass. Only rpc files changed.

## Plan refinement
- **Objective:** Preserve innocuous HTTP error metadata while redacting only the user's named credential headers and query fields, within rpc.
- **System fit:** HTTPError.Body remains bounded server data; Error stays status-only and transport URL wrappers remain omitted. Fact and other callers retain useful IDs and metadata.
- **Reuse and simplicity:** Narrow the existing redact helper with fixed credential names, keeping cookie parsing and authorization token handling; no configuration abstraction.
- **Evidence and unknowns:** User review identified overbroad query/header redaction; root tasks/lessons.md requires preserving intentional public error metadata. Literal redaction is not general server-secret detection.
- **Verification:** A real httptest error response echoes innocuous ID/header metadata plus all named header/query credentials. Prove metadata survives and credentials do not, then run offline race tests and vet.
- **Resource budget:** One focused helper change, regression and scoped documentation/lesson update; no module changes or dependencies.

## Credential redaction correction
- [x] Gate scoped plan and reproduce metadata loss.
- [x] Narrow redaction and update docs/lesson.
- [x] Run race tests and vet.

### Credential redaction review

The new real httptest failed before the fix because innocuous query ID, request
ID/header metadata and authentication scheme names disappeared. The narrowed
helper now preserves those values while redacting the four named credential
headers and three named query fields, including standalone auth/cookie secrets.
Status-only Error output is unchanged. Final offline `go test -race -count=1
./rpc` and `go vet ./rpc` pass. Changes remain scoped to rpc.
