# Runtime queue lessons

## 2026-11-12: Provider error text is not safe logging metadata

The runtime reviewer caught raw provider errors passed to slog. SDK errors can
include URLs with credentials. Log a controlled failure category plus explicit
cancellation/deadline flags, preserving the error for control flow without
printing its text. The worker failure test injects a credential-bearing URL and
asserts both the secret and provider URL stay out of captured logs.

## 2026-11-12: Replay identity belongs to a logical run

A process-wide run ID silently deduplicates future manual runs; generating one per
enqueue prevents stable retries of uncertain sends. Require typed Request.RunID
from the scheduler, reuse it across pages/retries, and use a fresh ID for new runs.
Keep only that field in the hashed delivery envelope; no static/random fallback.
