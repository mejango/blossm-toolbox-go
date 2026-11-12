package cryptography

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

type vectors struct {
	Source struct {
		MerklePatriciaTree string `json:"merklePatriciaTree"`
		RLP                string `json:"rlp"`
		Node               string `json:"node"`
	} `json:"source"`
	MPT []struct {
		Name  string      `json:"name"`
		Pairs [][2]string `json:"pairs"`
		Key   string      `json:"key"`
		Value *string     `json:"value"`
		Root  string      `json:"root"`
		Proof []string    `json:"proof"`
	} `json:"mpt"`
	RLP []struct {
		Name    string `json:"name"`
		Encoded string `json:"encoded"`
	} `json:"rlp"`
}

func loadVectors(t *testing.T) vectors {
	t.Helper()
	raw, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v vectors
	if err = json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	if v.Source.MerklePatriciaTree != "4.2.4" || v.Source.RLP != "3.0.0" || v.Source.Node != "v24.22.1" {
		t.Fatal("external vector provenance changed")
	}
	return v
}
func unhex(t *testing.T, s string) []byte {
	t.Helper()
	v, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func TestExternalMPTByteVectors(t *testing.T) {
	for _, v := range loadVectors(t).MPT {
		t.Run(v.Name, func(t *testing.T) {
			pairs := make([]Pair, len(v.Pairs))
			for i, pair := range v.Pairs {
				pairs[i] = Pair{Key: unhex(t, pair[0]), Value: unhex(t, pair[1])}
			}
			key := unhex(t, v.Key)
			expectedRoot := common.BytesToHash(unhex(t, v.Root))
			root, err := MPTRoot(pairs)
			if err != nil || root != expectedRoot {
				t.Fatalf("external root mismatch %s: %v", root.Hex(), err)
			}
			var proof [][]byte
			for _, node := range v.Proof {
				proof = append(proof, unhex(t, node))
			}
			value, err := VerifyMPTProof(expectedRoot, key, proof)
			if err != nil {
				t.Fatal("external proof rejected", err)
			}
			if v.Value == nil {
				if value != nil {
					t.Fatal("absence became value")
				}
			} else if !bytes.Equal(value, unhex(t, *v.Value)) {
				t.Fatal("external value bytes changed")
			}
			generatedRoot, generated, err := MPTProof(pairs, key)
			if err != nil || generatedRoot != expectedRoot {
				t.Fatal("proof generation root", err)
			}
			verified, err := VerifyMPTProof(root, key, generated)
			if err != nil || !bytes.Equal(verified, value) {
				t.Fatal("generated proof", err)
			}
			// Go omits nodes embedded in parents; every emitted node must nevertheless
			// be exactly one of the retained dependency's external RLP node encodings.
			for _, node := range generated {
				found := false
				for _, external := range proof {
					if bytes.Equal(node, external) {
						found = true
						break
					}
				}
				if !found {
					t.Fatal("proof node bytes diverged from retained implementation")
				}
			}
			if v.Value != nil {
				valid, err := VerifyMPT(pairs, key, unhex(t, *v.Value), root)
				if err != nil || !valid {
					t.Fatal("retained verify", err)
				}
				valid, err = VerifyMPT(pairs, key, []byte("wrong-value"), root)
				if err != nil || valid {
					t.Fatal("wrong value accepted", err)
				}
			} else {
				valid, err := VerifyMPT(pairs, key, nil, root)
				if err != nil || valid {
					t.Fatal("absence treated as empty-value membership", err)
				}
			}
			// Proof order is immaterial; verification is hash-addressed.
			for i, j := 0, len(proof)-1; i < j; i, j = i+1, j-1 {
				proof[i], proof[j] = proof[j], proof[i]
			}
			if checked, err := VerifyMPTProof(root, key, proof); err != nil || !bytes.Equal(checked, value) {
				t.Fatal("order-dependent proof", err)
			}
		})
	}
}
func TestMPTTamperingMissingNodeAndRootBinding(t *testing.T) {
	pairs := []Pair{{Key: []byte("a"), Value: []byte("b")}}
	root, nodes, err := MPTProof(pairs, []byte("a"))
	if err != nil || len(nodes) != 1 {
		t.Fatal(nodes, err)
	}
	if _, err = VerifyMPTProof(root, []byte("a"), nil); err == nil {
		t.Fatal("missing proof accepted")
	}
	changed := [][]byte{bytes.Clone(nodes[0])}
	changed[0][len(changed[0])-1] ^= 1
	if _, err = VerifyMPTProof(root, []byte("a"), changed); err == nil {
		t.Fatal("tampered proof accepted")
	}
	wrongRoot := root
	wrongRoot[0] ^= 1
	if _, err = VerifyMPTProof(wrongRoot, []byte("a"), nodes); err == nil {
		t.Fatal("wrong root accepted")
	}
	if value, err := VerifyMPTProof(root, []byte("missing"), nodes); err != nil || value != nil {
		t.Fatal("absence proof", err)
	}
	if _, err = VerifyMPTProof(root, []byte("a"), [][]byte{{}}); err == nil {
		t.Fatal("empty node accepted")
	}
	reversed := []Pair{{Key: []byte("b"), Value: []byte("2")}, {Key: []byte("a"), Value: []byte("1")}}
	a, err := MPTRoot(reversed)
	if err != nil {
		t.Fatal(err)
	}
	reversed[0], reversed[1] = reversed[1], reversed[0]
	b, err := MPTRoot(reversed)
	if err != nil || a != b {
		t.Fatal("root depends on unique-key insertion order", err)
	}
}
func TestExternalRLPByteVectors(t *testing.T) {
	values := map[string]any{
		"emptyString": "", "cat": "cat", "list": []any{"cat", "dog"}, "zero": uint64(0), "integer1024": uint64(1024),
		"nested": []any{[]any{}, "asdf", []any{[]any{}}, []any{[]any{}, []any{[]any{}}}}, "binary": []byte{0, 255, 128},
	}
	for _, v := range loadVectors(t).RLP {
		t.Run(v.Name, func(t *testing.T) {
			encoded, err := EncodeRLP(values[v.Name])
			if err != nil || !bytes.Equal(encoded, unhex(t, v.Encoded)) {
				t.Fatalf("external RLP mismatch: %x %v", encoded, err)
			}
			decoded, err := DecodeRLP(encoded)
			if err != nil {
				t.Fatal(err)
			}
			roundtrip, err := EncodeRLP(decoded)
			if err != nil || !bytes.Equal(encoded, roundtrip) {
				t.Fatal("RLP external bytes lost", err)
			}
		})
	}
}
func TestRLPRejectsNoncanonicalTruncatedAndTrailingBytes(t *testing.T) {
	for _, encoded := range [][]byte{nil, {0x81, 0x00}, {0x83, 'a'}, {0xc2, 0x80}, {0xb8, 0x01, 0x80}, {0x80, 0x80}} {
		if _, err := DecodeRLP(encoded); err == nil {
			t.Fatalf("accepted invalid RLP %x", encoded)
		}
	}
	var number uint64
	if err := DecodeRLPInto([]byte{0}, &number); err == nil {
		t.Fatal("noncanonical integer zero accepted")
	}
	if err := DecodeRLPInto([]byte{0x82, 0, 1}, &number); err == nil {
		t.Fatal("integer leading zero accepted")
	}
	for _, value := range []any{int64(-1), float64(1.5), map[string]string{"x": "y"}} {
		if _, err := EncodeRLP(value); err == nil {
			t.Fatal("unsupported RLP type accepted")
		}
	}
}
