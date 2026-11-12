package eventstore

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	eventlib "github.com/mejango/blossm-toolbox-go/lib/event"
)

func TestPublicationSizeReservesGeneratedFields(t *testing.T) {
	in := eventlib.Input{Payload: json.RawMessage(`""`)}
	if err := validateInputSize(in); err != nil {
		t.Fatal(err)
	}
	in.Payload, _ = json.Marshal(strings.Repeat("a", MaxPublicationBytes))
	if err := validateInputSize(in); !errors.Is(err, eventlib.ErrInvalid) {
		t.Fatalf("oversize accepted %v", err)
	}
	// Escaping, context and claims count too, not just raw payload length.
	in.Payload = json.RawMessage(`{}`)
	in.Context, _ = json.Marshal(map[string]string{"text": strings.Repeat("<", MaxPublicationBytes/5)})
	if err := validateInputSize(in); !errors.Is(err, eventlib.ErrInvalid) {
		t.Fatalf("escaped metadata accepted %v", err)
	}
}
