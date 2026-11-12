## 2026-11-12: Verify contention progress, not just consistency

The shared eight-writer append test exhausted the fixed five-attempt synchronized
retry schedule although the smaller adapter test allowed bounded contention
errors. Keep required concurrency success assertions intact. Test intentional
exhaustion separately, expose bounded retry configuration, jitter backoff, and
verify the actual shared workload rather than infer progress from gap-free counts.
