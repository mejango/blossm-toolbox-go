package mongo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// Chunking is an adapter wire detail. Each BSON document stays far below16MiB;
// the manifest/chunks share the caller's transaction and original JSON bytes.
const chunkBytes = 4 << 20

var ErrCorrupt = errors.New("mongo: corrupt stored payload")

type blobManifest struct {
	ID     bson.ObjectID `bson:"id"`
	Parts  int64         `bson:"parts"`
	Size   int64         `bson:"size"`
	Digest []byte        `bson:"sha256"`
}
type bodyChunk struct {
	Blob bson.ObjectID `bson:"blob"`
	Part int64         `bson:"part"`
	Body []byte        `bson:"body"`
}

func (m *blobManifest) validate() error {
	if m.ID.IsZero() || m.Size <= chunkBytes || m.Size > int64(^uint(0)>>1) || m.Parts != (m.Size-1)/chunkBytes+1 || len(m.Digest) != sha256.Size {
		return fmt.Errorf("%w: invalid chunk manifest", ErrCorrupt)
	}
	return nil
}
func bodyFields(doc document) bson.D {
	return bson.D{{Key: "body", Value: doc.Body}, {Key: "blob", Value: doc.Blob}}
}

// ctx already carries the owning session; never create an off-session upload.
func (t *transaction) writeBody(ctx context.Context, body []byte) (document, error) {
	if len(body) <= chunkBytes {
		return document{Body: body}, nil
	}
	digest := sha256.Sum256(body)
	m := &blobManifest{ID: bson.NewObjectID(), Parts: int64((len(body)-1)/chunkBytes + 1), Size: int64(len(body)), Digest: digest[:]}
	for part, start := int64(0), 0; start < len(body); part, start = part+1, start+chunkBytes {
		if err := ctx.Err(); err != nil {
			return document{}, err
		}
		end := min(start+chunkBytes, len(body))
		if _, err := t.backend.collection("payload_chunks").InsertOne(ctx, bodyChunk{Blob: m.ID, Part: part, Body: body[start:end]}); err != nil {
			return document{}, storageError(err)
		}
	}
	return document{Blob: m}, nil
}
func (r *reader) readBody(ctx context.Context, doc document) (body []byte, err error) {
	if doc.Blob == nil {
		if len(doc.Body) == 0 {
			return nil, fmt.Errorf("%w: missing body", ErrCorrupt)
		}
		return doc.Body, nil
	}
	m := doc.Blob
	if len(doc.Body) != 0 {
		return nil, fmt.Errorf("%w: both inline and chunked bodies", ErrCorrupt)
	}
	if err = m.validate(); err != nil {
		return nil, err
	}
	cursor, err := r.backend.collection("payload_chunks").Find(ctx, bson.D{{Key: "blob", Value: m.ID}}, options.Find().SetSort(bson.D{{Key: "part", Value: 1}}).SetBatchSize(2))
	if err != nil {
		return nil, storageError(err)
	}
	defer func() {
		c, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cancel()
		err = errors.Join(err, cursor.Close(c))
	}()
	digest := sha256.New()
	var part int64
	// Do not allocate the untrusted manifest's claimed length up front.
	for cursor.Next(ctx) {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		var chunk bodyChunk
		if err = cursor.Decode(&chunk); err != nil {
			return nil, err
		}
		want := int64(chunkBytes)
		if part == m.Parts-1 {
			want = m.Size - part*chunkBytes
		}
		if part >= m.Parts || chunk.Part != part || int64(len(chunk.Body)) != want || chunk.Blob != m.ID {
			return nil, fmt.Errorf("%w: invalid chunk order/length", ErrCorrupt)
		}
		body = append(body, chunk.Body...)
		_, _ = digest.Write(chunk.Body)
		part++
	}
	if err = cursor.Err(); err != nil {
		return nil, storageError(err)
	}
	if part != m.Parts || int64(len(body)) != m.Size || !bytes.Equal(digest.Sum(nil), m.Digest) {
		return nil, fmt.Errorf("%w: chunk count/length/hash mismatch", ErrCorrupt)
	}
	return body, ctx.Err()
}
func (t *transaction) deleteBody(ctx context.Context, doc document) error {
	if doc.Blob == nil {
		return nil
	}
	if err := doc.Blob.validate(); err != nil {
		return err
	}
	result, err := t.backend.collection("payload_chunks").DeleteMany(ctx, bson.D{{Key: "blob", Value: doc.Blob.ID}})
	if err != nil {
		return storageError(err)
	}
	if result.DeletedCount != doc.Blob.Parts {
		return fmt.Errorf("%w: incomplete chunk deletion", ErrCorrupt)
	}
	return nil
}
