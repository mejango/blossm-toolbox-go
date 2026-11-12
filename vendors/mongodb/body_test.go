package mongo

import (
	"crypto/sha256"
	"errors"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestManifestRejectsInvalidShape(t *testing.T) {
	good := blobManifest{ID: bson.NewObjectID(), Parts: 2, Size: chunkBytes + 1, Digest: make([]byte, sha256.Size)}
	if err := good.validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*blobManifest){func(m *blobManifest) { m.ID = bson.NilObjectID }, func(m *blobManifest) { m.Parts++ }, func(m *blobManifest) { m.Size = chunkBytes }, func(m *blobManifest) { m.Size = -1 }, func(m *blobManifest) { m.Digest = nil }} {
		bad := good
		change(&bad)
		if !errors.Is(bad.validate(), ErrCorrupt) {
			t.Fatal("corrupt manifest accepted", bad)
		}
	}
}
func TestMetadataBudgetIsConservative(t *testing.T) {
	if !boundedTerms(nil) || !boundedTerms([]string{strings.Repeat("x", (1<<20)-16)}) || boundedTerms([]string{strings.Repeat("x", 1<<20)}) || boundedTerms(make([]string, 10001)) {
		t.Fatal("metadata budget incorrect")
	}
}
