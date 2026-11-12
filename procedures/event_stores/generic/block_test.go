package eventstore

import (
	"context"
	"testing"

	canonicaljson "github.com/mejango/blossm-toolbox-go/lib/canonical_json"
	eventlib "github.com/mejango/blossm-toolbox-go/lib/event"
)

func TestBlockHashBinding(t *testing.T) {
	b := eventlib.Block{Number: 0, Events: []byte{0, 1, 255}, Snapshots: []byte{2, 3}, Transactions: []byte{4}}
	var e error
	b.Hash, e = canonicaljson.Digest(b)
	if e != nil {
		t.Fatal(e)
	}
	if e = VerifyBlock(context.Background(), b, nil, nil); e != nil {
		t.Fatal(e)
	}
	b.Events[0] = 9
	if e = VerifyBlock(context.Background(), b, nil, nil); e == nil {
		t.Fatal("accepted changed block bytes")
	}
}
