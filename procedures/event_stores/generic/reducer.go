package eventstore

import (
	"bytes"
	"encoding/json"

	eventlib "github.com/mejango/blossm-toolbox-go/lib/event"
)

// Reducers own their working memory. They cannot mutate hashed event bytes,
// reader buffers, or a prior state that a transaction retry may reuse.
func applyReducer(reducer eventlib.Reducer, state json.RawMessage, event eventlib.Event) (json.RawMessage, error) {
	event = eventlib.CloneEvent(event)
	return reducer.Apply(bytes.Clone(state), event)
}
