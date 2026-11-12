# EVM task lessons

The consumer needs a working encrypted custody adapter, not only an interface exercised by a test implementation. Supply standard AES-256-GCM with authenticated wallet metadata and separate key management, retaining the interface for cloud adapters. Parent owns cloud-durable journal integration; do not imply a local filesystem journal is a distributed deployment solution.

A durable immutable transaction must pass exact-request gas preflight before reserving its nonce. Receipt confirmations need parent-hash ancestry rather than unrelated canonical block and head reads. Regression fixtures enforce both rules.

The eventstore canonical protocol only permits safe53 JSON numbers. Persist EVM uint64 nonces and block/confirmation metadata as decimal strings so values above 2^53 survive event hashing and replay. Enforce the representation with a roundtrip fixture.

big.Int.SetInt64(0) truncates its slice without erasing the backing words. Clear
D.Bits() before resetting a private scalar, test the retained backing slice, and
continue describing cleanup as best-effort rather than guaranteed zeroization.
