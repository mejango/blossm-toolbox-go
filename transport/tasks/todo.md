# Shared HTTP no-content response

## Plan refinement

- **Objective:** Make shared write204 header-only and prove a real HTTP server returns no body/write error or second-header attempt.
- **System fit:** Command routes already select204; the shared transport writer owns status/body semantics. Keep JSON preencoding and error mapping for other statuses unchanged.
- **Reuse and simplicity:** Add one status branch in write and a real httptest server regression in a new test file. No route/framework/API changes.
- **Evidence and unknowns:** Current write marshals and writes JSON after204; net/http rejects this body, and wrap can attempt an error response after headers. Existing recorder serialization test does not exercise this server behavior.
- **Verification:** Reproduce serializable204 body rejection and marshal-only failure, then prove status204, empty body, nil write error and no server error log. Run scoped transport race/vet.
- **Resource budget:** Only transport/http.go, a new regression test and local workflow records. Use localhost httptest, no native DB/failpoints or overlapping transport edits.

- [x] Add header-only204 and real-server regression.
- [x] Pass scoped race/vet and report fresh results.

## Review

The real-server regression first failed both cases: serializable body returned
net/http ErrBodyNotAllowed after204, and an unserializable value returned500.
write now sets no-store and returns immediately after WriteHeader204, ignoring
the unused body. Other statuses retain JSON preencoding before headers.
TestWriteNoContentRealHTTP checks status204, empty body, nil writer error and
empty server error log for both inputs. Go1.27.4 scoped transport race covering
that test, serialization/boundary and Framework tests passes1.760s; transport vet
passes. No existing tests, Framework routes or other source files were edited.
