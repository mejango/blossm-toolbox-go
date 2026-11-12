# Runnable framework composition

`cmd/framework` runs this application using the generic command, fact, job,
store, view and projection owners. `New` borrows the supplied database and cloud
clients; the executable owns their shutdown. Identity and document stores require
separate database namespaces so their outbox workers cannot consume each other's
records.

`POST /commands/set` commits or queues an owner-bound document. Authenticated
principal context comes from Dia, never the JSON body. Existing ownership is
checked before queue admission and again by the transactional reducer. The
repeatable `Then` response works for committed, duplicate and queued outcomes.
Facts, jobs and named views use the same authenticated owner. Job verification
also compares the injected claims subject and principal.

Optional `Config.Outbound` enables `POST /commands/copy` with payload
`{"source":"one","sourceVersion":0,"destination":"copied"}`. Its Main reads that exact historical version of an owned document,
then calls the configured service's `set` command through `CommandClient`.
`Config.Delegated` accepts only a separately configured peer credential on command
routes, with mandatory transaction binding and current Dia session validation.
The executable configures a fixed target, service-specific audience and peer
subject. Child commitment is independent of the parent HTTP response; retries
reuse its stable actor-bound event identity. No distributed rollback is promised.

Remote command delivery requires an explicit `DeliveryVerifier`. There is no
fallback to store-peer credentials. Without a verifier `/deliveries` is absent.
Valid deterministic poison deliveries are acknowledged only after an immutable
rejection record commits in the local backend. Uncertain/network/canceled outcomes
remain retryable. Queue acceptance returns its stable `envelopeId`; the runtime
logs that ID and the fixed reason after a fresh rejection commits. This log is
best effort and can be lost after a crash, while the journal remains authoritative.
The internal peer can inspect the two-field record at
`GET /delivery-rejections/{envelopeId}`; ordinary users cannot.

Manual replay requires `runId` and `limit` at
`POST /projections/documents/replay`. Reuse the run ID for retries/pages and select
a new one for a new rebuild. Local and explicitly inline modes invoke projection
work directly; remote modes require a configured producer. Local mode retains its
publication bypass.

Verification: `integration/framework_flow_test.go` uses real PostgreSQL and MongoDB,
real HTTP gateways, actual command/store replay, signed nested service calls,
projection/outbox recovery, queue adapter requests, replay generations and session
revocation. Provider clients use local SDK seams, not live cloud resources.

```sh
GOTOOLCHAIN=go1.27.4 go test -race ./serviceapp
GOTOOLCHAIN=go1.27.4 go test -race -tags=integration ./integration -run TestGeneric
```

The native command requires `POSTGRES_URL` and `MONGODB_URI` for disposable local
fixtures. Container tests additionally exercise the built `/framework` executable.
