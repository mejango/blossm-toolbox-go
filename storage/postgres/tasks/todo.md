## Plan refinement

- **Objective:** Make eight concurrent shared PostgreSQL appends succeed using configurable bounded jittered retries, without changing shared assertions or uncertain-commit handling.
- **System fit:** Storage owns retry scheduling; parent append callbacks remain repeatable and retain idempotency/hash ownership. Only adapter files change, with no deployment or module edits.
- **Reuse and simplicity:** Reuse Update and pgx error classification, standard math/rand/v2 and context timers. Preserve Open and add an explicit constructor accepting retry settings.
- **Evidence and unknowns:** Parent shared concurrency test reports three exhausted serialization errors under the fixed five-attempt schedule. Successful smaller adapter tests did not establish eight-writer progress.
- **Verification:** Test validation, delay bounds, explicit attempt exhaustion and cancellation. Run unchanged shared eight-writer concurrency repeatedly under race detection and adapter retry/concurrency tests.
- **Resource budget:** One bounded local implementation and focused PostgreSQL fixture runs at port55439. Keep five-attempt exhaustion as configurable behavior, use ten attempts by default, cap exponential delays and jitter each wait.

- [x] Implement retry configuration and jitter.
- [x] Pass focused unit/native and unchanged shared concurrency tests.

## Review

Implemented `RetryConfig` and `OpenWithRetry`, preserving `Open`. Defaults are ten attempts with 10ms initial and 250ms capped exponential delay ceilings, independently half-to-full jittered. Unknown commits remain non-retryable.

The unchanged shared `TestNativeContract/postgres/concurrent-counts` passed 20 consecutive race-enabled runs on the parent localhost55439 fixture (7.134s). Focused adapter retry/concurrency/uncertain-commit/lock-cancellation checks and ordinary tests/vet pass. Intentional exhaustion uses explicit three-attempt configuration; adapter concurrent writers now require every writer to succeed. Cancellation and disabled-retry regression also passed. No shared assertions, module files or files outside the adapter were changed.
