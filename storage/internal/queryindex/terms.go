// Package queryindex projects canonical snapshot state into indexed scalar terms.
// Arrays do not add path segments, matching eventstore.Matches traversal.
package queryindex

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"

	"github.com/mejango/blossm-toolbox-go/stores/event"
)

// State returns deduplicated typed leaves. The original state is never mutated.
// A literal dot in an object key is not a path delimiter here: that key cannot
// be addressed by Matches' dotted-path syntax and must not alias nested fields.
func State(raw json.RawMessage) ([]string, error) {
	// encoding/json serializes a nil RawMessage as JSON null. Preserve that
	// existing low-level snapshot contract in the separate query projection.
	if raw == nil {
		raw = json.RawMessage(`null`)
	}
	normalized, err := eventstore.Canonical(raw)
	if err != nil {
		return nil, err
	}
	d := json.NewDecoder(bytes.NewReader(normalized))
	d.UseNumber()
	var value any
	if err = d.Decode(&value); err != nil {
		return nil, err
	}
	terms := map[string]struct{}{}
	walk(value, []string{}, terms)
	result := make([]string, 0, len(terms))
	for term := range terms {
		result = append(result, term)
	}
	sort.Strings(result)
	return result, nil
}
func walk(value any, path []string, terms map[string]struct{}) {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			walk(child, append(path, key), terms)
		}
	case []any:
		for _, child := range v {
			walk(child, path, terms)
		}
	default:
		terms[term(path, v)] = struct{}{}
	}
}
func term(path []string, value any) string {
	// All values come from the canonical JSON decoder, so Marshal cannot fail.
	b, _ := json.Marshal([]any{path, value})
	return string(b)
}

// Predicates uses the same canonical numeric spelling as Matches. Scalar type
// is part of each term, including explicit null; missing paths have no term.
func Predicates(predicates []eventstore.Predicate) ([]string, error) {
	if _, err := eventstore.Matches(json.RawMessage(`{}`), predicates); err != nil {
		return nil, err
	}
	result := make([]string, 0, len(predicates))
	for _, p := range predicates {
		raw, err := eventstore.Canonical(p.Value)
		if err != nil {
			return nil, err
		}
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		var value any
		if err = d.Decode(&value); err != nil {
			return nil, err
		}
		result = append(result, term(strings.Split(p.Path, "."), value))
	}
	return result, nil
}
