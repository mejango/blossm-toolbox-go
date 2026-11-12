//go:build integration

package mongo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/mejango/blossm-toolbox-go/stores/event"
	"go.mongodb.org/mongo-driver/v2/bson"
)

type smallStateReducer struct{}

func (smallStateReducer) Normalize(_ string, p json.RawMessage) (json.RawMessage, error) {
	return p, nil
}
func (smallStateReducer) Apply(_ json.RawMessage, e eventstore.Event) (json.RawMessage, error) {
	return eventstore.Encode(map[string]int64{"count": e.Number + 1})
}

func TestNativeLargeTailBlockPreservesBytes(t *testing.T) {
	b, ctx := fixture(t)
	root := testRoot("large-tail")
	s := eventstore.Store{Backend: b, Local: true, Reducers: map[string]eventstore.Reducer{"service/domain": smallStateReducer{}}}
	// Each input/publication fits the parent's240KiB envelope bound; only their
	// combined tail and block exceed BSON's per-document ceiling.
	padding := strings.Repeat("x", 180<<10)
	payload, err := json.Marshal(map[string]string{"padding": padding})
	must(t, err)
	inputs := make([]eventstore.Input, 100)
	for i := range inputs {
		inputs[i] = eventstore.Input{Root: root, Key: fmt.Sprintf("large-%d", i), Action: "count", Created: "2026-11-12T00:00:00Z", Payload: payload, Transaction: eventstore.Transaction{ID: fmt.Sprintf("tx-%d", i), Issued: "2026-11-12T00:00:00Z"}}
	}
	_, err = s.Append(ctx, inputs)
	must(t, err)
	block, err := s.BuildBlock(ctx, 1, nil)
	must(t, err)
	wire, err := json.Marshal(block)
	must(t, err)
	if len(block.Events) <= 16<<20 || len(wire) <= 16<<20 {
		t.Fatal("fixture did not exceed BSON limit", len(block.Events), len(wire))
	}
	must(t, b.View(ctx, func(r eventstore.Reader) error {
		got, err := r.LastBlock(ctx)
		if err != nil {
			return err
		}
		encoded, err := json.Marshal(got)
		if err != nil {
			return err
		}
		if !bytes.Equal(encoded, wire) {
			t.Fatal("large block bytes changed")
		}
		hash := got.Hash
		got.Hash = ""
		actual, err := eventstore.Digest(got)
		if err != nil {
			return err
		}
		if actual != hash {
			t.Fatal("block hash changed")
		}
		snapshot, err := r.Snapshot(ctx, root)
		if err != nil {
			return err
		}
		if len(snapshot.Events) <= 16<<20 || string(snapshot.State) != `{"count":100}` {
			t.Fatal("tail lost or state grew", len(snapshot.Events), string(snapshot.State))
		}
		return nil
	}))
	aggregate, err := s.Aggregate(ctx, root)
	must(t, err)
	if aggregate.Version != 99 || string(aggregate.State) != `{"count":100}` {
		t.Fatal(aggregate.Version, string(aggregate.State))
	}
	var parent document
	must(t, b.collection("blocks").FindOne(ctx, id(block.Number)).Decode(&parent))
	if parent.Blob == nil || len(parent.Body) != 0 {
		t.Fatal("oversized body remained inline")
	}
	count, err := b.collection("payload_chunks").CountDocuments(ctx, bson.D{{Key: "blob", Value: parent.Blob.ID}})
	must(t, err)
	if count != parent.Blob.Parts {
		t.Fatal("chunk count mismatch", count, parent.Blob.Parts)
	}
}

func TestNativeLargeStateCandidatesRemainComplete(t *testing.T) {
	b, ctx := fixture(t)
	root := testRoot("large-state")
	largeValue := strings.Repeat("s", 17<<20)
	state, err := json.Marshal(map[string]string{"data": largeValue, "tag": "yes"})
	must(t, err)
	snapshot := eventstore.Snapshot{Root: root, Number: 0, LastEvent: 0, State: state, Events: []byte{0, 255}}
	snapshot.Hash, err = eventstore.Digest(snapshot)
	must(t, err)
	must(t, b.Update(ctx, func(tx eventstore.Tx) error {
		if err := tx.LockRoots(ctx, []eventstore.Root{root}); err != nil {
			return err
		}
		if err := tx.PutCount(ctx, root, 1); err != nil {
			return err
		}
		return tx.PutSnapshot(ctx, snapshot)
	}))
	var head struct {
		Unindexed bool     `bson:"unindexed"`
		Terms     []string `bson:"state_terms"`
	}
	must(t, b.collection("snapshot_heads").FindOne(ctx, id(root.Key())).Decode(&head))
	if !head.Unindexed || len(head.Terms) != 0 {
		t.Fatal("oversized index terms were not bounded")
	}
	s := eventstore.Store{Backend: b, Reducers: map[string]eventstore.Reducer{"service/domain": smallStateReducer{}}}
	matches, err := s.Query(ctx, root.Network, root.Service, root.Domain, []eventstore.Predicate{{Path: "tag", Value: json.RawMessage(`"yes"`)}})
	must(t, err)
	if len(matches) != 1 || !bytes.Equal(matches[0].State, state) {
		t.Fatal("unindexed clean snapshot omitted or changed")
	}
	matches, err = s.Query(ctx, root.Network, root.Service, root.Domain, []eventstore.Predicate{{Path: "tag", Value: json.RawMessage(`"no"`)}})
	must(t, err)
	if len(matches) != 0 {
		t.Fatal("conservative candidate escaped final Matches")
	}
	value, err := json.Marshal(largeValue)
	must(t, err)
	must(t, b.View(ctx, func(r eventstore.Reader) error {
		roots, err := r.(eventstore.CandidateReader).Candidates(ctx, root.Network, root.Service, root.Domain, []eventstore.Predicate{{Path: "data", Value: value}})
		if len(roots) != 1 || roots[0] != root {
			t.Fatal("oversized query predicate lost candidate", roots)
		}
		return err
	}))
}
func chunkCount(t *testing.T, b *Backend, ctx context.Context, filter bson.D) int64 {
	t.Helper()
	n, err := b.collection("payload_chunks").CountDocuments(ctx, filter)
	must(t, err)
	return n
}
func stored(t *testing.T, b *Backend, ctx context.Context, collection string, key any) document {
	t.Helper()
	var doc document
	must(t, b.collection(collection).FindOne(ctx, id(key)).Decode(&doc))
	return doc
}
func blobFilter(doc document) bson.D { return bson.D{{Key: "blob", Value: doc.Blob.ID}} }

func TestNativeChunkReplacementDeletionAndRollback(t *testing.T) {
	b, ctx := fixture(t)
	root := testRoot("mutable")
	body, err := json.Marshal(map[string]string{"data": strings.Repeat("b", chunkBytes+64)})
	must(t, err)
	event := testEvent(root, 0)
	event.Input.Payload = body
	event.Fingerprint, err = eventstore.Digest(event.Input)
	must(t, err)
	event.Hash = ""
	event.Hash, err = eventstore.Digest(event)
	must(t, err)
	p := eventstore.Projection{ID: "large-projection", Root: root, Version: 1, Body: body}
	must(t, b.Update(ctx, func(tx eventstore.Tx) error {
		if err := appendRaw(ctx, tx, event); err != nil {
			return err
		}
		_, err := tx.PutProjection(ctx, p)
		return err
	}))
	publication := stored(t, b, ctx, "publications", event.Hash)
	projection := stored(t, b, ctx, "projections", p.ID)
	eventDoc := stored(t, b, ctx, "events", event.Input.Key)
	if publication.Blob == nil || projection.Blob == nil || eventDoc.Blob == nil {
		t.Fatal("fixtures did not use chunks")
	}
	must(t, b.View(ctx, func(r eventstore.Reader) error {
		got, err := r.Find(ctx, event.Input.Key)
		if err != nil {
			return err
		}
		if err = eventstore.Verify(got); err != nil {
			return err
		}
		n := 0
		if err = r.Events(ctx, root, -1, func(e eventstore.Event) error { n++; return eventstore.Verify(e) }); err != nil {
			return err
		}
		if n != 1 {
			t.Fatal(n)
		}
		rows, err := r.(*reader).Projections(ctx, root.Network, root.Service, root.Domain, "", 10)
		if len(rows) != 1 || !bytes.Equal(rows[0].Body, body) {
			t.Fatal("chunked projection listing changed")
		}
		return err
	}))
	before := chunkCount(t, b, ctx, bson.D{})
	stop := errors.New("rollback cleanup")
	err = b.Update(ctx, func(tx eventstore.Tx) error {
		if err := tx.DeletePublication(ctx, event.Hash); err != nil {
			return err
		}
		next := p
		next.Version = 2
		next.Body = json.RawMessage(`{}`)
		if _, err := tx.PutProjection(ctx, next); err != nil {
			return err
		}
		return stop
	})
	if !errors.Is(err, stop) || chunkCount(t, b, ctx, bson.D{}) != before {
		t.Fatal("rollback lost chunks", err)
	}
	must(t, b.Update(ctx, func(tx eventstore.Tx) error { return tx.FailPublication(ctx, event.Hash) }))
	must(t, b.View(ctx, func(r eventstore.Reader) error {
		pending, err := r.Pending(ctx, 10)
		if len(pending) != 1 || pending[0].Attempts != 1 || !bytes.Equal(pending[0].Event.Input.Payload, body) {
			t.Fatal("pending chunk read changed")
		}
		return err
	}))
	p.Version = 2
	must(t, b.Update(ctx, func(tx eventstore.Tx) error { _, err := tx.PutProjection(ctx, p); return err }))
	newer := stored(t, b, ctx, "projections", p.ID)
	if newer.Blob == nil || newer.Blob.ID == projection.Blob.ID || chunkCount(t, b, ctx, blobFilter(projection)) != 0 {
		t.Fatal("old projection chunks leaked")
	}
	err = b.Update(ctx, func(tx eventstore.Tx) error {
		v := p
		v.Version = 3
		if _, err := tx.PutProjection(ctx, v); err != nil {
			return err
		}
		return stop
	})
	if !errors.Is(err, stop) || chunkCount(t, b, ctx, blobFilter(newer)) != newer.Blob.Parts {
		t.Fatal("rollback replacement lost previous chunks", err)
	}
	p.Version = 3
	p.Body = json.RawMessage(`{}`)
	must(t, b.Update(ctx, func(tx eventstore.Tx) error {
		if _, err := tx.PutProjection(ctx, p); err != nil {
			return err
		}
		return tx.DeletePublication(ctx, event.Hash)
	}))
	if chunkCount(t, b, ctx, blobFilter(newer)) != 0 || chunkCount(t, b, ctx, blobFilter(publication)) != 0 || chunkCount(t, b, ctx, blobFilter(eventDoc)) != eventDoc.Blob.Parts {
		t.Fatal("cleanup leaked or deleted another record's chunks")
	}
	small := stored(t, b, ctx, "projections", p.ID)
	if small.Blob != nil || len(small.Body) == 0 {
		t.Fatal("large-to-inline replacement retained blob")
	}
}

func TestNativeChunkInsertRollbackAndCorruption(t *testing.T) {
	b, ctx := fixture(t)
	block := eventstore.Block{Number: 0, Hash: "block", Events: bytes.Repeat([]byte{1}, chunkBytes)}
	stop := errors.New("abort chunks")
	err := b.Update(ctx, func(tx eventstore.Tx) error {
		if err := tx.PutBlock(ctx, block); err != nil {
			return err
		}
		return stop
	})
	if !errors.Is(err, stop) || chunkCount(t, b, ctx, bson.D{}) != 0 {
		t.Fatal("aborted chunks leaked", err)
	}
	must(t, b.Update(ctx, func(tx eventstore.Tx) error { return tx.PutBlock(ctx, block) }))
	before := chunkCount(t, b, ctx, bson.D{})
	duplicate := block
	duplicate.Number = 1
	err = b.Update(ctx, func(tx eventstore.Tx) error { return tx.PutBlock(ctx, duplicate) })
	if !errors.Is(err, eventstore.ErrConflict) || chunkCount(t, b, ctx, bson.D{}) != before {
		t.Fatal("unique failure leaked chunks", err)
	}
	for i, kind := range []string{"missing", "extra", "order", "length", "hash", "manifest-size", "manifest-parts", "manifest-hash", "both-bodies"} {
		t.Run(kind, func(t *testing.T) {
			current := block
			current.Number = int64(i + 1)
			current.Hash = kind
			must(t, b.Update(ctx, func(tx eventstore.Tx) error { return tx.PutBlock(ctx, current) }))
			doc := stored(t, b, ctx, "blocks", current.Number)
			m := doc.Blob
			first := append(blobFilter(doc), bson.E{Key: "part", Value: int64(0)})
			var err error
			switch kind {
			case "missing":
				_, err = b.collection("payload_chunks").DeleteOne(ctx, first)
			case "extra":
				_, err = b.collection("payload_chunks").InsertOne(ctx, bodyChunk{Blob: m.ID, Part: m.Parts, Body: []byte{1}})
			case "order":
				_, err = b.collection("payload_chunks").UpdateOne(ctx, first, bson.M{"$set": bson.M{"part": int64(-1)}})
			case "length":
				_, err = b.collection("payload_chunks").UpdateOne(ctx, first, bson.M{"$set": bson.M{"body": []byte{1}}})
			case "hash":
				var chunk bodyChunk
				must(t, b.collection("payload_chunks").FindOne(ctx, first).Decode(&chunk))
				chunk.Body[0] ^= 1
				_, err = b.collection("payload_chunks").UpdateOne(ctx, first, bson.M{"$set": bson.M{"body": chunk.Body}})
			case "manifest-size":
				_, err = b.collection("blocks").UpdateOne(ctx, id(current.Number), bson.M{"$set": bson.M{"blob.size": m.Size + 1}})
			case "manifest-parts":
				_, err = b.collection("blocks").UpdateOne(ctx, id(current.Number), bson.M{"$set": bson.M{"blob.parts": m.Parts + 1}})
			case "manifest-hash":
				_, err = b.collection("blocks").UpdateOne(ctx, id(current.Number), bson.M{"$set": bson.M{"blob.sha256": make([]byte, 32)}})
			case "both-bodies":
				_, err = b.collection("blocks").UpdateOne(ctx, id(current.Number), bson.M{"$set": bson.M{"body": []byte(`null`)}})
			}
			must(t, err)
			err = b.View(ctx, func(r eventstore.Reader) error { _, err := r.LastBlock(ctx); return err })
			if !errors.Is(err, ErrCorrupt) {
				t.Fatal("corrupt body accepted", kind, err)
			}
		})
	}
}
