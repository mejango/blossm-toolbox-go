package audit

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"testing"

	canonicaljson "github.com/mejango/blossm-toolbox-go/lib/canonical_json"
	eventlib "github.com/mejango/blossm-toolbox-go/lib/event"
)

func TestPrivateExport(t *testing.T) {
	ctx := context.Background()
	key := make([]byte, 32)
	if _, e := rand.Read(key); e != nil {
		t.Fatal(e)
	}
	producer, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	signer := ES256{Private: producer}
	b := eventlib.Block{Events: []byte{0, 255, 1}, Snapshots: []byte("private"), Transactions: []byte("transaction")}
	b.Hash, e = canonicaljson.Digest(b)
	if e != nil {
		t.Fatal(e)
	}
	sealed, e := Seal(ctx, b, "producer1", key, signer)
	if e != nil {
		t.Fatal(e)
	}
	opened, e := Open(ctx, sealed, "producer1", key, ES256{Public: &producer.PublicKey})
	if e != nil || opened.Hash != b.Hash || string(opened.Events) != string(b.Events) {
		t.Fatal(opened, e)
	}
	if _, e = Open(ctx, sealed, "other", key, signer); e == nil {
		t.Fatal("foreign producer accepted")
	}
	sealed.Ciphertext[0] ^= 1
	if _, e = Open(ctx, sealed, "producer1", key, signer); e == nil {
		t.Fatal("tampered ciphertext accepted")
	}
	if _, e = Seal(ctx, b, "producer1", key, nil); e == nil {
		t.Fatal("unsigned export accepted")
	}
}
