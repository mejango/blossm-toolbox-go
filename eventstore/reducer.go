package eventstore

import (
	"bytes"
	"encoding/json"
	"slices"
)

func cloneInput(in Input) Input {
	in.Payload = bytes.Clone(in.Payload)
	in.Context = bytes.Clone(in.Context)
	in.Transaction.Claims = bytes.Clone(in.Transaction.Claims)
	in.Transaction.Path = slices.Clone(in.Transaction.Path)
	if in.Expected != nil {
		expected := *in.Expected
		in.Expected = &expected
	}
	return in
}

// Reducers own their working memory. They cannot mutate hashed event bytes,
// reader buffers, or a prior state that a transaction retry may reuse.
func applyReducer(reducer Reducer, state json.RawMessage, event Event) (json.RawMessage, error) {
	event = CloneEvent(event)
	return reducer.Apply(bytes.Clone(state), event)
}

// CloneEvent detaches mutable metadata for callbacks without changing the
// committed event representation or either integrity hash.
func CloneEvent(event Event) Event {
	event.Input = cloneInput(event.Input)
	return event
}
