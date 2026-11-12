package evm

import (
	"encoding/json"
	"testing"

	"github.com/mejango/blossm-toolbox-go/stores/event"
)

func TestPersistedEVMUint64UsesDecimalStrings(t *testing.T) {
	nonce := ^uint64(0)
	prepared := Prepared{ID: "uint64", Nonce: nonce}
	raw, err := json.Marshal(prepared)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := eventstore.Canonical(raw)
	if err != nil {
		t.Fatal("nonce exceeds safe53 number encoding", err)
	}
	var decoded Prepared
	if err = json.Unmarshal(canonical, &decoded); err != nil || decoded.Nonce != nonce {
		t.Fatal("nonce lost precision", decoded.Nonce, err)
	}
	observation := Observation{BlockNumber: nonce, Confirmations: nonce}
	raw, err = json.Marshal(observation)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err = eventstore.Canonical(raw)
	if err != nil {
		t.Fatal("observation exceeds safe53 number encoding", err)
	}
	var restored Observation
	if err = json.Unmarshal(canonical, &restored); err != nil || restored.BlockNumber != nonce || restored.Confirmations != nonce {
		t.Fatal("block evidence lost precision", restored, err)
	}
}
