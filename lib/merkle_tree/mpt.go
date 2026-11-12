// Package merkletree owns retained Ethereum Merkle Patricia trie byte contracts.
package merkletree

import (
	"bytes"
	"errors"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethdb/memorydb"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/ethereum/go-ethereum/triedb"
)

// Pair contains exact key/value bytes. Keys are NOT hashed before insertion.
// Pairs apply sequentially: later duplicate keys replace earlier values, and
// empty values delete keys, matching Ethereum trie and retained BaseTrie behavior.
type Pair struct{ Key, Value []byte }

func withTrie(pairs []Pair, fn func(*trie.Trie) error) error {
	disk := rawdb.NewMemoryDatabase()
	defer disk.Close()
	database := triedb.NewDatabase(disk, nil)
	defer database.Close()
	tree := trie.NewEmpty(database)
	for _, pair := range pairs {
		if err := tree.Update(bytes.Clone(pair.Key), bytes.Clone(pair.Value)); err != nil {
			return err
		}
	}
	return fn(tree)
}

func MPTRoot(pairs []Pair) (common.Hash, error) {
	var root common.Hash
	err := withTrie(pairs, func(tree *trie.Trie) error { root = tree.Hash(); return nil })
	return root, err
}

// MPTProof returns standard RLP-encoded proof nodes, sorted by their Keccak hash.
// Node order is immaterial to verification. Nodes embedded in their parents need
// no separate entry. The empty trie has the Ethereum empty root and no nodes.
func MPTProof(pairs []Pair, key []byte) (common.Hash, [][]byte, error) {
	var root common.Hash
	nodes := [][]byte{}
	err := withTrie(pairs, func(tree *trie.Trie) error {
		root = tree.Hash()
		proof := memorydb.New()
		defer proof.Close()
		if err := tree.Prove(key, proof); err != nil {
			return err
		}
		iterator := proof.NewIterator(nil, nil)
		defer iterator.Release()
		for iterator.Next() {
			nodes = append(nodes, bytes.Clone(iterator.Value()))
		}
		return iterator.Error()
	})
	return root, nodes, err
}

// VerifyMPTProof verifies external proof bytes against the trusted root and key.
// nil,nil proves absence; a nonnil error means the proof cannot establish the
// claim. This is not the same as comparing an absent key with an empty value.
func VerifyMPTProof(root common.Hash, key []byte, nodes [][]byte) ([]byte, error) {
	if root == types.EmptyRootHash {
		if len(nodes) != 0 {
			return nil, errors.New("empty trie proof must contain no nodes")
		}
		return nil, nil
	}
	proof := memorydb.New()
	defer proof.Close()
	for _, node := range nodes {
		if len(node) == 0 {
			return nil, errors.New("empty MPT proof node")
		}
		if err := proof.Put(crypto.Keccak256(node), bytes.Clone(node)); err != nil {
			return nil, err
		}
	}
	value, err := trie.VerifyProof(root, key, proof)
	return bytes.Clone(value), err
}

// VerifyMPT preserves the retained rebuild-and-verify convenience operation,
// comparing exact value bytes rather than converting potentially binary data to text.
func VerifyMPT(pairs []Pair, key, value []byte, root common.Hash) (bool, error) {
	_, nodes, err := MPTProof(pairs, key)
	if err != nil {
		return false, err
	}
	found, err := VerifyMPTProof(root, key, nodes)
	if err != nil {
		return false, err
	}
	return found != nil && bytes.Equal(found, value), nil
}
