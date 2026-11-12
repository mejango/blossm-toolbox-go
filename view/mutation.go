// Package view owns scoped projection reads and versioned view mutations.
package view

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"blossm.local/backend/eventstore"
)

type absent struct{}

type Op string

const (
	Set      Op = "set"
	Unset    Op = "unset"
	Inc      Op = "inc"
	Push     Op = "push"
	AddToSet Op = "addToSet"
	Pull     Op = "pull"
)

// Operation addresses body fields only. Paths use dots, existing numeric array
// indexes, $[] for every element, or $[name] with a named object predicate filter.
// Pull accepts either an exact JSON Value or a conjunction of object predicates.
type Operation struct {
	Kind    Op                                `json:"kind"`
	Path    string                            `json:"path"`
	Value   json.RawMessage                   `json:"value,omitempty"`
	Where   []eventstore.Predicate            `json:"where,omitempty"`
	Filters map[string][]eventstore.Predicate `json:"filters,omitempty"`
}

// Apply composes operations in supplied order without mutating the input. A
// parent array replacement followed by a filtered child update is deterministic.
func Apply(body json.RawMessage, operations []Operation) (json.RawMessage, error) {
	value, err := objectBody(body)
	if err != nil {
		return nil, invalid(err)
	}
	return applyOperations(value, operations)
}

// Parsing remains unclassified: the same body can be caller input (Apply) or
// persisted state (Store patches). The caller owns that distinction.
func objectBody(body json.RawMessage) (any, error) {
	value, err := decode(body)
	if err != nil {
		return nil, err
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, fmt.Errorf("view body must be an object")
	}
	return value, nil
}
func applyOperations(value any, operations []Operation) (json.RawMessage, error) {
	for _, op := range operations {
		path, err := op.validate()
		if err != nil {
			return nil, invalid(err)
		}
		value, _, err = op.edit(value, path)
		if err != nil {
			return nil, invalid(fmt.Errorf("%s %s: %w", op.Kind, op.Path, err))
		}
	}
	encoded, err := eventstore.Encode(value)
	return encoded, invalid(err)
}
func decode(raw json.RawMessage) (any, error) {
	canonical, err := eventstore.Canonical(raw)
	if err != nil {
		return nil, err
	}
	d := json.NewDecoder(bytes.NewReader(canonical))
	d.UseNumber()
	var value any
	err = d.Decode(&value)
	return value, err
}
func predicatesValid(p []eventstore.Predicate) error {
	_, err := eventstore.Matches(json.RawMessage(`{}`), p)
	return err
}
func (op Operation) validate() ([]string, error) {
	path := strings.Split(op.Path, ".")
	used := map[string]bool{}
	for i, part := range path {
		if part == "" {
			return nil, fmt.Errorf("empty mutation path segment")
		}
		if strings.HasPrefix(part, "$") {
			if i == 0 || !strings.HasPrefix(part, "$[") || !strings.HasSuffix(part, "]") {
				return nil, fmt.Errorf("invalid array selector %q", part)
			}
			name := part[2 : len(part)-1]
			if name != "" {
				p, ok := op.Filters[name]
				if !ok || len(p) == 0 {
					return nil, fmt.Errorf("missing array filter %q", name)
				}
				used[name] = true
			}
		}
	}
	for name, p := range op.Filters {
		if !used[name] {
			return nil, fmt.Errorf("unused array filter %q", name)
		}
		if err := predicatesValid(p); err != nil {
			return nil, err
		}
	}
	if len(op.Where) > 0 && (op.Kind != Pull || len(op.Value) > 0) {
		return nil, fmt.Errorf("where is only valid for pull without value")
	}
	if err := predicatesValid(op.Where); err != nil {
		return nil, err
	}
	switch op.Kind {
	case Unset:
		if len(op.Value) > 0 {
			return nil, fmt.Errorf("unset takes no value")
		}
	case Pull:
		if len(op.Where) > 0 {
			return path, nil
		}
		fallthrough
	case Set, Push, AddToSet, Inc:
		value, err := decode(op.Value)
		if err != nil {
			return nil, err
		}
		if op.Kind == Inc {
			if _, ok := value.(json.Number); !ok {
				return nil, fmt.Errorf("increment must be numeric")
			}
		}
	default:
		return nil, fmt.Errorf("unsupported mutation %q", op.Kind)
	}
	return path, nil
}
func (op Operation) edit(node any, path []string) (any, bool, error) {
	if len(path) == 0 {
		return op.leaf(node)
	}
	switch current := node.(type) {
	case map[string]any:
		part := path[0]
		if strings.HasPrefix(part, "$") {
			return nil, false, fmt.Errorf("array selector on object")
		}
		child, exists := current[part]
		if !exists {
			child = absent{}
		}
		if !exists && (op.Kind == Unset || op.Kind == Pull) {
			return node, false, nil
		}
		if !exists && len(path) > 1 {
			child = map[string]any{}
		}
		next, remove, err := op.edit(child, path[1:])
		if err != nil {
			return nil, false, err
		}
		if remove {
			delete(current, part)
		} else {
			current[part] = next
		}
		return current, false, nil
	case []any:
		return op.editArray(current, path)
	default:
		return nil, false, fmt.Errorf("path traverses a non-container")
	}
}
func (op Operation) editArray(array []any, path []string) (any, bool, error) {
	part := path[0]
	if strings.HasPrefix(part, "$[") {
		name := part[2 : len(part)-1]
		for i, item := range array {
			if name != "" {
				raw, err := eventstore.Encode(item)
				if err != nil {
					return nil, false, err
				}
				matches, err := eventstore.Matches(raw, op.Filters[name])
				if err != nil {
					return nil, false, err
				}
				if !matches {
					continue
				}
			}
			next, remove, err := op.edit(item, path[1:])
			if err != nil {
				return nil, false, err
			}
			if remove {
				next = nil
			}
			array[i] = next
		}
		return array, false, nil
	}
	index, err := strconv.Atoi(part)
	if err != nil || index < 0 || index >= len(array) || strconv.Itoa(index) != part {
		return nil, false, fmt.Errorf("array index must identify an existing element")
	}
	next, remove, err := op.edit(array[index], path[1:])
	if err != nil {
		return nil, false, err
	}
	if remove {
		next = nil
	}
	array[index] = next
	return array, false, nil
}
func (op Operation) leaf(current any) (any, bool, error) {
	if op.Kind == Unset {
		return nil, true, nil
	}
	if op.Kind == Pull {
		value, err := op.pull(current)
		return value, false, err
	}
	// Decode anew for each selected element so nested values never alias siblings.
	value, err := decode(op.Value)
	if err != nil {
		return nil, false, err
	}
	switch op.Kind {
	case Set:
		return value, false, nil
	case Inc:
		if _, missing := current.(absent); missing {
			current = json.Number("0")
		}
		number, ok := current.(json.Number)
		if !ok {
			return nil, false, fmt.Errorf("increment target must be numeric")
		}
		before, err := number.Float64()
		if err != nil {
			return nil, false, err
		}
		delta, err := value.(json.Number).Float64()
		if err != nil {
			return nil, false, err
		}
		return json.Number(strconv.FormatFloat(before+delta, 'g', -1, 64)), false, nil
	case Push, AddToSet:
		if _, missing := current.(absent); missing {
			current = []any{}
		}
		array, ok := current.([]any)
		if !ok {
			return nil, false, fmt.Errorf("array operation target must be an array")
		}
		if op.Kind == AddToSet {
			for _, item := range array {
				if equal(item, value) {
					return array, false, nil
				}
			}
		}
		return append(array, value), false, nil
	}
	return nil, false, fmt.Errorf("unsupported operation")
}
func equal(a, b any) bool {
	x, _ := eventstore.Encode(a)
	y, _ := eventstore.Encode(b)
	return bytes.Equal(x, y)
}
func (op Operation) pull(current any) (any, error) {
	array, ok := current.([]any)
	if !ok {
		return nil, fmt.Errorf("pull target must be an array")
	}
	var value any
	var err error
	if len(op.Where) == 0 {
		value, err = decode(op.Value)
		if err != nil {
			return nil, err
		}
	}
	result := make([]any, 0, len(array))
	for _, item := range array {
		remove := equal(item, value)
		if len(op.Where) > 0 {
			raw, e := eventstore.Encode(item)
			if e != nil {
				return nil, e
			}
			remove, err = eventstore.Matches(raw, op.Where)
			if err != nil {
				return nil, err
			}
		}
		if !remove {
			result = append(result, item)
		}
	}
	return result, nil
}
