package query

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	canonicaljson "github.com/mejango/blossm-toolbox-go/lib/canonical_json"
	eventlib "github.com/mejango/blossm-toolbox-go/lib/event"
)

func matchPath(v any, path []string, want any) bool {
	if a, ok := v.([]any); ok {
		for _, x := range a {
			if matchPath(x, path, want) {
				return true
			}
		}
		return false
	}
	if len(path) == 0 {
		a, _ := json.Marshal(v)
		b, _ := json.Marshal(want)
		return bytes.Equal(a, b)
	}
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	next, ok := m[path[0]]
	return ok && matchPath(next, path[1:], want)
}

// Matches is conjunction of strict scalar equalities. Explicit null differs
// from absence; a path through an array matches any element.
func Matches(state json.RawMessage, predicates []eventlib.Predicate) (bool, error) {
	d := json.NewDecoder(bytes.NewReader(state))
	d.UseNumber()
	var obj any
	if e := d.Decode(&obj); e != nil {
		return false, e
	}
	matched := true
	for _, p := range predicates {
		for _, part := range strings.Split(p.Path, ".") {
			if part == "" {
				return false, fmt.Errorf("empty query path segment")
			}
		}
		if p.Path == "" {
			return false, fmt.Errorf("empty query path")
		}
		raw, e := canonicaljson.Canonical(p.Value)
		if e != nil {
			return false, e
		}
		d = json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		var want any
		if e = d.Decode(&want); e != nil {
			return false, e
		}
		switch want.(type) {
		case map[string]any, []any:
			return false, fmt.Errorf("query value must be scalar")
		}
		if !matchPath(obj, strings.Split(p.Path, "."), want) {
			matched = false
		}
	}
	return matched, nil
}
