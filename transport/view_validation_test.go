package transport

import (
	"context"
	"errors"
	"net/url"
	"testing"

	"blossm.local/backend/eventstore"
)

type failingViewBackend struct{ eventstore.Backend }

func (failingViewBackend) View(context.Context, func(eventstore.Reader) error) error {
	return errors.New("invalid backend encoding: private detail")
}
func (failingViewBackend) Update(context.Context, func(eventstore.Tx) error) error {
	return errors.New("invalid backend encoding: private detail")
}

func TestViewGatewayValidationVersusBackendFailure(t *testing.T) {
	h, err := (ViewGateway{Authorize: gatewayAuth, Stores: map[string]eventstore.Backend{"items": gatewayFixture(), "failed": failingViewBackend{}}}).Handler()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{
		{"GET", "/view-stores/items?query=" + url.QueryEscape(`{"Predicates":[{"Path":"n","Value":{}}]}`), "", 400},
		{"POST", "/view-stores/items/batch", `{"mutations":[{"id":"a","root":{"network":"n","service":"s","domain":"d","id":"a"},"version":-1,"body":{"owner":"alice"}}]}`, 400},
		{"POST", "/view-stores/items/batch", `{"mutations":[{"id":"a","root":{"network":"n","service":"s","domain":"d","id":"a"},"version":2,"expected":1,"operations":[{"kind":"push","path":"n","value":1}]}]}`, 400},
		{"GET", "/view-stores/failed/a", "", 500},
		{"POST", "/view-stores/failed/batch", `{"mutations":[{"id":"a","root":{"network":"n","service":"s","domain":"d","id":"a"},"version":2,"body":{"owner":"alice"}}]}`, 500},
	} {
		w := gatewayRequest(h, tc.method, tc.path, tc.body, true, true)
		if w.Code != tc.status {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, w.Code, w.Body.String())
		}
		if tc.status == 500 && w.Body.String() != "{\"error\":\"internal error\"}\n" {
			t.Fatalf("backend details leaked: %s", w.Body.String())
		}
	}
}
