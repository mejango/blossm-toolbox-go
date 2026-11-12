# Fact procedures and clients

`Procedure.Read(ctx, Request)` validates required authenticated contexts and query
JSON before calling `ReadFn`. `Procedure.Stream(ctx, Request, Yield)` uses the same
checks, invokes `StreamFn`, and serializes/awaits every callback. The command
package owns the shared required-context rule. Authentication, authorization,
query schema validation and HTTP routing remain consumer responsibilities.

`StreamFn` must await/check yield errors and honor its context during application
work. A callback failure is latched, cancels the producer context and prevents
further callbacks even if a producer ignores the error. Concurrent producers are
serialized, in-flight callbacks finish before Stream returns, and late emissions
are rejected. Producers retain responsibility for closing their database cursors
or other resources, normally through defer. Callback code must honor context;
arbitrary user code cannot be forcibly cancelled.

`Client{RPC: client}` offers `Read`, `Stream` and `StreamRaw`, each taking explicit
path, `url.Values` query and `http.Header` credentials. Reads parse JSON; structured
streams decode complete JSON objects/arrays across arbitrary HTTP chunk and UTF-8
boundaries. Truncated/malformed JSON, invalid UTF-8 and scalar stream records fail.
Use StreamRaw for explicit binary/text chunks. Every stream execution owns its
decoder and awaits each callback before consuming the next record.

RPC configuration owns total request deadline and maximum response bytes, including
streamed data. Callbacks receive the RPC request context, so this deadline also
reaches callback processing. HTTP failures reject before callbacks. Callback,
transport, decoding or cancellation errors close the response body and cancel the
request. There is no automatic reconnect or replay: earlier records may already
have been processed when a later error occurs, so application checkpoints and
idempotency belong to the consumer.

Run `go test -race ./fact ./rpc` and `go vet ./fact ./rpc` from the parent module.
Actual httptest fixtures cover split multibyte records, serial callback completion,
server disconnect cleanup, callback deadlines, bad JSON/UTF-8, status failures,
body bounds, raw streams and concurrent local producers. No cloud imports or
network services are needed.
