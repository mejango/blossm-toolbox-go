# Transport lessons

## 2026-11-12: Reuse both sides of the stream contract

The first view RPC test emitted a scalar while fact.Client.Stream accepts only
JSON objects and arrays. A matching wire format includes producer validation,
not just newline encoding. Reuse fact.Procedure.Stream as well as its client to
retain serial callbacks, sticky emit errors, cancellation and closed emitters.
Test scalar rejection, ignored callback errors and callbacks after completion.

## 2026-11-12: An HTTP200 and zero-valued struct are not an acknowledgment

Go JSON decoding accepts null and missing fields without proving an operation
outcome. Validate required field presence/types and consistency against the
requested operation before publishing a decoded result. Preserve valid partial
progress alongside errors, but keep malformed responses from supplying trusted
commit/completion flags. Cover empty/no-op success and failure cases separately.

## 2026-11-12: No-content status needs a real HTTP assertion

A response recorder does not establish net/http's body prohibition for204.
Branch before serialization and body writing when the selected contract has no
content. Test through a real server, capture the returned write error and server
log, and assert no second response attempt as well as an empty wire body.
