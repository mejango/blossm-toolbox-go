package view

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"

	eventlib "github.com/mejango/blossm-toolbox-go/lib/event"
)

type ValueKind string

const (
	NumberValue ValueKind = "number"
	DateValue   ValueKind = "date"
	StringValue ValueKind = "string"
)

// Bound uses JSON numbers for numeric ranges and RFC3339 strings for dates.
// Missing bounds are unbounded; explicit null is invalid. No string coercion.
type Bound struct {
	Value     json.RawMessage `json:"value"`
	Exclusive bool            `json:"exclusive,omitempty"`
}
type Range struct {
	Path  string    `json:"path"`
	Kind  ValueKind `json:"kind"`
	Lower *Bound    `json:"lower,omitempty"`
	Upper *Bound    `json:"upper,omitempty"`
}
type TextQuery struct {
	Paths []string `json:"paths"`
	Value string   `json:"value"`
}
type Sort struct {
	Path string    `json:"path"`
	Kind ValueKind `json:"kind"`
	Desc bool      `json:"desc,omitempty"`
}

type orderedValue struct {
	number *big.Rat
	date   time.Time
	text   string
}

func asOrdered(v any, kind ValueKind) (orderedValue, bool) {
	switch kind {
	case NumberValue:
		n, ok := v.(json.Number)
		if !ok {
			return orderedValue{}, false
		}
		r, ok := new(big.Rat).SetString(string(n))
		return orderedValue{number: r}, ok
	case DateValue:
		s, ok := v.(string)
		if !ok {
			return orderedValue{}, false
		}
		d, err := time.Parse(time.RFC3339Nano, s)
		return orderedValue{date: d}, err == nil
	case StringValue:
		s, ok := v.(string)
		return orderedValue{text: s}, ok
	}
	return orderedValue{}, false
}
func compare(a, b orderedValue, kind ValueKind) int {
	switch kind {
	case NumberValue:
		return a.number.Cmp(b.number)
	case DateValue:
		return a.date.Compare(b.date)
	default:
		return strings.Compare(a.text, b.text)
	}
}
func validPath(path string) error {
	return predicatesValid([]eventlib.Predicate{{Path: path, Value: json.RawMessage(`null`)}})
}
func boundValue(b *Bound, kind ValueKind) (orderedValue, error) {
	v, err := decode(b.Value)
	if err != nil {
		return orderedValue{}, err
	}
	out, ok := asOrdered(v, kind)
	if !ok {
		return out, fmt.Errorf("range bound must be %s", kind)
	}
	return out, nil
}
func (q Query) validate() error {
	if q.Limit < 1 || q.Limit > MaxLimit || q.Offset < 0 {
		return fmt.Errorf("invalid view pagination")
	}
	if err := predicatesValid(q.Predicates); err != nil {
		return err
	}
	for _, r := range q.Ranges {
		if err := validPath(r.Path); err != nil {
			return err
		}
		if r.Kind != NumberValue && r.Kind != DateValue {
			return fmt.Errorf("invalid range kind")
		}
		if r.Lower == nil && r.Upper == nil {
			return fmt.Errorf("range needs a bound")
		}
		var lo, hi orderedValue
		var err error
		if r.Lower != nil {
			lo, err = boundValue(r.Lower, r.Kind)
			if err != nil {
				return err
			}
		}
		if r.Upper != nil {
			hi, err = boundValue(r.Upper, r.Kind)
			if err != nil {
				return err
			}
		}
		if r.Lower != nil && r.Upper != nil && compare(lo, hi, r.Kind) > 0 {
			return fmt.Errorf("range bounds reversed")
		}
	}
	if q.Text != nil {
		if strings.TrimSpace(q.Text.Value) == "" || len(q.Text.Paths) == 0 {
			return fmt.Errorf("text requires a value and body paths")
		}
		for _, p := range q.Text.Paths {
			if err := validPath(p); err != nil {
				return err
			}
		}
	}
	for _, s := range q.Sort {
		if err := validPath(s.Path); err != nil {
			return err
		}
		if s.Kind != NumberValue && s.Kind != DateValue && s.Kind != StringValue {
			return fmt.Errorf("invalid sort kind")
		}
	}
	return nil
}

// Reuse the existing dotted traversal. Flatten terminal arrays to preserve
// Matches' scalar-any-element semantics; absent values remain distinct from null.
func scalarValues(body any, path string) []any {
	var out []any
	var add func(any)
	add = func(v any) {
		if a, ok := v.([]any); ok {
			for _, x := range a {
				add(x)
			}
		} else {
			out = append(out, v)
		}
	}
	for _, v := range csvPathValues(body, strings.Split(path, ".")) {
		add(v)
	}
	return out
}
func (r Range) matches(body any) bool {
	var lo, hi orderedValue
	if r.Lower != nil {
		lo, _ = boundValue(r.Lower, r.Kind)
	}
	if r.Upper != nil {
		hi, _ = boundValue(r.Upper, r.Kind)
	}
	for _, v := range scalarValues(body, r.Path) {
		x, ok := asOrdered(v, r.Kind)
		if !ok {
			continue
		}
		if r.Lower != nil {
			c := compare(x, lo, r.Kind)
			if c < 0 || (c == 0 && r.Lower.Exclusive) {
				continue
			}
		}
		if r.Upper != nil {
			c := compare(x, hi, r.Kind)
			if c > 0 || (c == 0 && r.Upper.Exclusive) {
				continue
			}
		}
		return true
	}
	return false
}
func (q Query) matchesExtra(p eventlib.Projection) (bool, error) {
	if len(q.Ranges) == 0 && q.Text == nil {
		return true, nil
	}
	body, err := decode(p.Body)
	if err != nil {
		return false, err
	}
	for _, r := range q.Ranges {
		if !r.matches(body) {
			return false, nil
		}
	}
	if q.Text == nil || p.ID == q.Text.Value {
		return true, nil
	}
	needle := strings.ToLower(q.Text.Value)
	for _, path := range q.Text.Paths {
		for _, v := range scalarValues(body, path) {
			if s, ok := v.(string); ok && strings.Contains(strings.ToLower(s), needle) {
				return true, nil
			}
		}
	}
	return false, nil
}
func sortValue(body any, s Sort) *orderedValue {
	var best *orderedValue
	for _, v := range scalarValues(body, s.Path) {
		x, ok := asOrdered(v, s.Kind)
		if !ok {
			continue
		}
		if best == nil {
			best = &x
			continue
		}
		c := compare(x, *best, s.Kind)
		if (!s.Desc && c < 0) || (s.Desc && c > 0) {
			best = &x
		}
	}
	return best
}
