# Authenticated jobs

`Input` carries Payload, Context and Claims. Framework injects trusted Context and
Claims from its Authorize result, replacing caller body values. Standalone
`Procedure.Handler` requires `func(*http.Request) (job.Identity, error)` and does
the same replacement. Authenticators must validate service/scheduler identity
and audience before constructing Identity. Direct Execute callers own that trust
boundary. Client.Execute uses the shared RPC transport, with explicit headers,
no hidden retries, and a required 204 acknowledgment.

Run `go test -race ./job` and `go vet ./job`. Actual httptest verifies both access
rejection and replacement of forged context/claims before the job runs.
