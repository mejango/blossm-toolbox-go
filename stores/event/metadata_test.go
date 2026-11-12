package eventstore

import (
	"encoding/json"
	"testing"
)

type objectReducer struct{}

func (objectReducer) Normalize(_ string, p json.RawMessage) (json.RawMessage, error) {
	return Canonical(p)
}
func (objectReducer) Apply(_ json.RawMessage, e Event) (json.RawMessage, error) {
	return e.Input.Payload, nil
}
func TestMetadataNormalization(t *testing.T) {
	s := Store{Reducers: map[string]Reducer{"core/test": objectReducer{}}}
	base := Input{Root: Root{Network: "n", Service: "core", Domain: "test", ID: "id"}, Key: "key", Action: "set", Created: "2026-11-12T01:00:00+01:00", Payload: json.RawMessage(`{}`), Transaction: Transaction{ID: "tx", Issued: "2026-11-12T00:00:00Z", Claims: json.RawMessage(`{"exp":1800000000,"aud":["n"],"custom":{}}`)}}
	a, e := s.normalize(base)
	if e != nil {
		t.Fatal(e)
	}
	b, e := s.normalize(a)
	if e != nil {
		t.Fatal(e)
	}
	ha, _ := Digest(a)
	hb, _ := Digest(b)
	if ha != hb || a.Created != "2026-11-12T00:00:00Z" {
		t.Fatal("unstable normalization")
	}
	for _, claims := range []string{`null`, `[]`, `{"exp":"1800000000"}`, `{"nbf":-1}`, `{"aud":["n",1]}`, `{"aud":[]}`} {
		bad := base
		bad.Transaction.Claims = json.RawMessage(claims)
		if _, e := s.normalize(bad); e == nil {
			t.Fatalf("accepted claims %s", claims)
		}
	}
	bad := base
	bad.Context = json.RawMessage(`null`)
	if _, e := s.normalize(bad); e == nil {
		t.Fatal("accepted null context")
	}
	bad = base
	bad.Transaction.Path = []Trace{{Network: "n"}}
	if _, e := s.normalize(bad); e == nil {
		t.Fatal("accepted incomplete trace")
	}
}

func TestInvalidUTF8MetadataCannotBeSilentlyRepaired(t *testing.T) {
	s := Store{Reducers: map[string]Reducer{"core/test": objectReducer{}}}
	base := Input{Root: Root{Network: "n", Service: "core", Domain: "test", ID: "id"}, Key: "key", Action: "set", Created: "2026-11-12T00:00:00Z", Payload: json.RawMessage(`{}`), Transaction: Transaction{ID: "tx", Issued: "2026-11-12T00:00:00Z"}}
	bad := string([]byte{0xff})
	for _, mutate := range []func(*Input){
		func(in *Input) { in.Root.ID = bad }, func(in *Input) { in.Key = bad }, func(in *Input) { in.Action = bad }, func(in *Input) { in.Transaction.ID = bad }, func(in *Input) {
			in.Transaction.Path = []Trace{{Network: "n", Host: "h", Procedure: "p", Hash: "h", Name: bad}}
		},
	} {
		in := base
		mutate(&in)
		if _, err := s.normalize(in); err == nil {
			t.Fatal("invalid metadata repaired by JSON marshal")
		}
	}
}
