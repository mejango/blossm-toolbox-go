# Retained Ethereum MPT and RLP

This package owns the byte contracts retained from Toolbox lib/merkle_tree and
lib/rlp. It uses go-ethereum v1.17.10, the same parent dependency as EVM, without
implementing a trie, hash algorithm, proof decoder or RLP codec locally.

- `MPTRoot([]Pair)` returns an Ethereum Merkle Patricia trie root.
- `MPTProof(pairs, key)` returns that root and standard RLP-encoded proof nodes.
- `VerifyMPTProof(root, key, nodes)` verifies external proof bytes and returns the
  exact stored value. A nil value with nil error proves absence; an error means
  the proof could not establish the claim.
- `VerifyMPT(pairs, key, value, root)` retains the rebuild-and-verify convenience
  operation, using exact byte comparison. Absence never counts as membership.
- `EncodeRLP(value)`, `DecodeRLP(bytes)` and `DecodeRLPInto(bytes, destination)`
  delegate canonical Ethereum encoding and decoding to the SDK.

Keys and values are raw bytes. String callers explicitly use `[]byte(text)` for
UTF-8; binary values are never decoded through a lossy text conversion. Keys are
not prehashed. Pairs apply in sequence: later duplicate keys overwrite earlier
ones; empty values delete keys. Empty keys are supported. A fresh in-memory trie
is rebuilt per operation, matching the retained package's lifecycle. No disk,
network, cloud, service discovery or eventstore block mutation occurs.

Generated proof nodes are sorted by Keccak hash, and may omit nodes already
embedded in parents. Ordering is irrelevant to Ethereum proof verification.
External proofs may include those embedded nodes. The empty trie uses Ethereum's
standard empty root with zero proof nodes. Verification requires a trusted root;
a valid proof does not establish who authorized that root.

RLP byte strings decode to `[]byte`, lists to `[]any`. Use unsigned Go integers
for numeric encoding, strings for UTF-8 text, and byte slices for arbitrary data.
Typed decoding additionally enforces integer canonicality. Inputs must contain
exactly one complete canonical RLP value with no trailing data. Callers own input
size limits at their transport boundary; these pure functions consume supplied
in-memory byte slices.

## API evidence and compatibility fixtures

Official v1.17.10 sources inspected on 2026-11-12:

- [Trie construction, Update and Hash](https://github.com/ethereum/go-ethereum/blob/v1.17.10/trie/trie.go).
- [Prove and VerifyProof](https://github.com/ethereum/go-ethereum/blob/v1.17.10/trie/proof.go).
- [Trie database construction](https://github.com/ethereum/go-ethereum/blob/v1.17.10/triedb/database.go).
- [In-memory database](https://github.com/ethereum/go-ethereum/blob/v1.17.10/core/rawdb/database.go).
- [RLP exact-byte decoder](https://github.com/ethereum/go-ethereum/blob/v1.17.10/rlp/decode.go).

[testdata/vectors.json](testdata/vectors.json) was generated offline with the
retained installed merkle-patricia-tree 4.2.4 and rlp 3.0.0, using Node 24.22.1.
It fixes exact roots, external proof node bytes and RLP bytes independently of the
Go implementation. [testdata/generate.cjs](testdata/generate.cjs) regenerates it
from those already-installed dependencies without installing or downloading any
packages. A dependency version change requires deliberate vector review.

Go tests compare all roots and values against those external vectors, verify
external and generated proofs, compare emitted proof-node bytes with the external
nodes, and reject tampering, missing nodes, wrong roots and malformed RLP. Empty,
binary, prefix, overwrite, deletion and nested-list cases are included.

Run `go test -race ./cryptography` and `go vet ./cryptography` from the parent
module. On 2026-11-12, Go 1.27.4 compiled this package and the complete race
suite and scoped vet passed against go-ethereum v1.17.10, including the external
byte vectors.
