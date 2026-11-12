package transport

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWriteNoContentRealHTTP(t *testing.T) {
	for _, tc := range []struct {
		name string
		body any
	}{{"serializable", map[string]string{"ignored": "body"}}, {"unserializable", make(chan int)}} {
		t.Run(tc.name, func(t *testing.T) {
			var serverLog bytes.Buffer
			result := make(chan error, 1)
			server := httptest.NewUnstartedServer(wrap(func(w http.ResponseWriter, r *http.Request) error {
				err := write(w, http.StatusNoContent, tc.body)
				result <- err
				return err
			}))
			server.Config.ErrorLog = log.New(&serverLog, "", 0)
			server.Start()
			t.Cleanup(server.Close)
			response, err := server.Client().Get(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			body, readErr := io.ReadAll(response.Body)
			response.Body.Close()
			writeErr := <-result
			server.Close() // Complete handler/error logging before inspecting the buffer.
			if response.StatusCode != http.StatusNoContent || len(body) != 0 || readErr != nil || writeErr != nil {
				t.Fatalf("status=%d body=%q read=%v write=%v", response.StatusCode, body, readErr, writeErr)
			}
			if response.Header.Get("Cache-Control") != "no-store" {
				t.Fatal(response.Header)
			}
			if serverLog.Len() != 0 {
				t.Fatalf("server attempted a second response: %s", serverLog.String())
			}
		})
	}
}
