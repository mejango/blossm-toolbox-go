package event

import (
	"bytes"
	"slices"
)

func CloneInput(in Input) Input {
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

// CloneEvent detaches mutable metadata for callbacks without changing the
// committed event representation or either integrity hash.
func CloneEvent(event Event) Event {
	event.Input = CloneInput(event.Input)
	return event
}
