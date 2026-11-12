package view

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"blossm.local/backend/eventstore"
)

func TestFormatIntegrity(t *testing.T) {
	p := eventstore.Projection{ID: "original", Root: eventstore.Root{ID: "source", Network: "network"}, Version: 7, Body: json.RawMessage(`{"z":2,"a":{"value":1}}`)}
	before, _ := json.Marshal(p)
	var callbackBody json.RawMessage
	doc, err := Format(p, func(copy eventstore.Projection) (json.RawMessage, error) {
		copy.ID = "forged"
		copy.Root.ID = "forged"
		copy.Version = 99
		copy.Body[0] = '['
		callbackBody = json.RawMessage(`{ "headers": {"id":"forged"}, "a": 1.0 }`)
		return callbackBody, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if doc.Headers != (Headers{ID: p.ID, Root: p.Root, Version: p.Version}) {
		t.Fatalf("headers changed: %+v", doc.Headers)
	}
	if string(doc.Body) != `{"a":1,"headers":{"id":"forged"}}` {
		t.Fatalf("body: %s", doc.Body)
	}
	callbackBody[0] = '['
	if doc.Body[0] != '{' {
		t.Fatal("output aliases callback bytes")
	}
	after, _ := json.Marshal(p)
	if !bytes.Equal(before, after) {
		t.Fatal("formatter mutated original projection")
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if len(wire) != 2 || wire["headers"] == nil || !bytes.Equal(wire["body"], doc.Body) {
		t.Fatalf("incorrect envelope: %s", raw)
	}

	doc, err = Format(p, nil)
	if err != nil || string(doc.Body) != `{"a":{"value":1},"z":2}` {
		t.Fatalf("default: %s, %v", doc.Body, err)
	}
	doc.Body[0] = '['
	if !bytes.Equal(p.Body, json.RawMessage(`{"z":2,"a":{"value":1}}`)) {
		t.Fatal("default body aliases input")
	}
}

func TestFormatErrors(t *testing.T) {
	sentinel := errors.New("formatter failed")
	p := eventstore.Projection{Body: json.RawMessage(`{}`)}
	if _, err := Format(p, func(eventstore.Projection) (json.RawMessage, error) { return nil, sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("formatter error: %v", err)
	}
	p.Deleted = true
	if _, err := Format(p, func(eventstore.Projection) (json.RawMessage, error) {
		t.Fatal("deleted formatter called")
		return nil, nil
	}); !errors.Is(err, eventstore.ErrNotFound) {
		t.Fatalf("deleted: %v", err)
	}
	for _, raw := range []string{"", `{`, `{"a":1,"a":2}`, `{} {}`, `9007199254740992`, `"\ud800"`, "\"\xff\""} {
		t.Run(raw, func(t *testing.T) {
			if _, err := Format(eventstore.Projection{Body: json.RawMessage(raw)}, nil); err == nil {
				t.Fatal("accepted invalid original")
			}
			if _, err := Format(eventstore.Projection{Body: json.RawMessage(`{}`)}, func(eventstore.Projection) (json.RawMessage, error) { return json.RawMessage(raw), nil }); err == nil {
				t.Fatal("accepted invalid output")
			}
		})
	}
	for _, raw := range []string{`null`, `false`, `-3`, `"text"`, `[1,2]`} {
		doc, err := Format(eventstore.Projection{Body: json.RawMessage(raw)}, nil)
		if err != nil || string(doc.Body) != raw {
			t.Fatalf("valid scalar/array %s: %s, %v", raw, doc.Body, err)
		}
	}
}

func TestCSVValuesAndPaths(t *testing.T) {
	document := Document{Headers: Headers{ID: "real-id"}, Body: json.RawMessage(`{
  "id":"forged", "text":"a,\"b\"\nnext", "nil":null,"number":-3.0,"bool":true,
  "nested":{"z":2,"a":1}, "array":[2,1], "empty":[],
  "people":[{"name":"Ada"},{"name":"Lin"},{}],
  "groups":[{"people":[{"n":1},{"n":2}]},{"people":[{"n":3}]}],
  "single":[{"name":"=formula"}], "nulls":[{"n":null},{"n":2}]
 }`)}
	paths := []string{"text", "nil", "missing", "number", "bool", "nested", "array", "empty", "people.name", "groups.people.n", "single.name", "nulls.n", "empty.name", "number.child"}
	columns := make([]Column, len(paths))
	for i, path := range paths {
		columns[i] = Column{Name: path, Path: path}
	}
	var output bytes.Buffer
	if err := CSV(&output, []Document{document}, columns); err != nil {
		t.Fatal(err)
	}
	records, err := csv.NewReader(&output).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{append(paths, "id"), {"a,\"b\"\nnext", "", "", "-3", "true", `{"a":1,"z":2}`, `[2,1]`, `[]`, `["Ada","Lin"]`, `[1,2,3]`, "'=formula", `[null,2]`, "", "", "real-id"}}
	if !reflect.DeepEqual(records, want) {
		t.Fatalf("records %#v; want %#v", records, want)
	}
}

func TestCSVSpreadsheetStrings(t *testing.T) {
	dangerous := []string{"=1+1", "+1+1", "-1+1", "@SUM(1)", "  =1", "\tplain", "\rplain", "\nplain", "\x00=1", "\u0085=1", "\u007f@x", "\u009f+x", "＝1", "＋1", "－1", "＠SUM(1)", "\ufeff\t＝1", "\u00a0-1", "\u2028+1"}
	ordinary := []string{"ordinary", "a=1", "'=-1", "  text", "\x00plain", "a,\"=1\nnext"}
	for _, value := range append(dangerous, ordinary...) {
		t.Run(value, func(t *testing.T) {
			body, _ := json.Marshal(map[string]string{"value": value})
			var output bytes.Buffer
			if err := CSV(&output, []Document{{Headers: Headers{ID: value}, Body: body}}, []Column{{Name: value, Path: "value"}}); err != nil {
				t.Fatal(err)
			}
			records, err := csv.NewReader(&output).ReadAll()
			if err != nil {
				t.Fatal(err)
			}
			want := value
			for _, danger := range dangerous {
				if value == danger {
					want = "'" + value
				}
			}
			if !reflect.DeepEqual(records, [][]string{{want, "id"}, {want, want}}) {
				t.Fatalf("got %#v, want shielded %q", records, want)
			}
		})
	}
}

func TestCSVValidationBeforeWriting(t *testing.T) {
	cases := [][]Column{
		{{Name: "", Path: "a"}}, {{Name: " ", Path: "a"}}, {{Name: "id", Path: "a"}},
		{{Name: "a", Path: "a"}, {Name: "a", Path: "b"}},
		{{Name: "a", Path: ""}}, {{Name: "a", Path: "a..b"}}, {{Name: "a", Path: ".a"}}, {{Name: "a", Path: "a."}},
	}
	for _, columns := range cases {
		var output bytes.Buffer
		if err := CSV(&output, nil, columns); err == nil || output.Len() != 0 {
			t.Fatalf("invalid columns %+v: %v, %q", columns, err, output.String())
		}
	}
	for _, raw := range []string{"", `{"a":1,"a":2}`, `{} {}`, `"\ud800"`, `9007199254740992`} {
		var output bytes.Buffer
		err := CSV(&output, []Document{{Body: json.RawMessage(`{"a":1}`)}, {Body: json.RawMessage(raw)}}, []Column{{Name: "a", Path: "a"}})
		if err == nil || output.Len() != 0 {
			t.Fatalf("invalid body %q: %v, %q", raw, err, output.String())
		}
	}
	var output bytes.Buffer
	if err := CSV(&output, nil, nil); err != nil || output.String() != "id\n" {
		t.Fatalf("empty export: %q, %v", output.String(), err)
	}
}

type csvFailWriter struct{ err error }

func (w csvFailWriter) Write([]byte) (int, error) { return 0, w.err }

func TestCSVWriterFailure(t *testing.T) {
	sentinel := errors.New("write failed")
	for _, value := range []string{"small", strings.Repeat("x", 8192)} {
		body, _ := json.Marshal(map[string]string{"value": value})
		err := CSV(csvFailWriter{sentinel}, []Document{{Body: body}}, []Column{{Name: "value", Path: "value"}})
		if !errors.Is(err, sentinel) {
			t.Fatalf("writer error: %v", err)
		}
	}
	if err := CSV(io.Discard, nil, []Column{{Name: "value", Path: "value"}}); err != nil {
		t.Fatal(err)
	}
}
