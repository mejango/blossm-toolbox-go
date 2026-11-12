package dwolla

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Header: http.Header{"Location": {"https://api-sandbox.dwolla.com/created-id"}}, Body: io.NopCloser(strings.NewReader(body))}
}
func client(t *testing.T, fn func(*http.Request) *http.Response) *Client {
	t.Helper()
	c, err := New("https://api-sandbox.dwolla.com", "key", "secret", &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" && (r.GetBody != nil || r.Body == nil || r.Body == http.NoBody) {
			t.Error("side effect is replayable")
		}
		if r.URL.Path == "/token" {
			u, p, ok := r.BasicAuth()
			if !ok || u != "key" || p != "secret" {
				t.Error("bad token authentication")
			}
			b, _ := io.ReadAll(r.Body)
			if string(b) != "grant_type=client_credentials" {
				t.Error("bad grant")
			}
			return response(200, `{"access_token":"token","token_type":"bearer","expires_in":3600}`), nil
		}
		if r.Header.Get("Authorization") != "Bearer token" || r.Header.Get("Accept") != media {
			t.Error("bad API headers")
		}
		return fn(r), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func TestCustomers(t *testing.T) {
	for _, kind := range []string{"", "receive-only", "personal", "business"} {
		t.Run(kind, func(t *testing.T) {
			c := client(t, func(r *http.Request) *http.Response {
				if r.Method != "POST" || r.URL.Path != "/customers" || r.Header.Get("Idempotency-Key") != "retry-key" {
					t.Error("bad create request")
				}
				var body Customer
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if body.Type != kind {
					t.Error("type lost")
				}
				if kind == "business" && (body.Controller == nil || body.Controller.FirstName != "Jane" || body.Controller.Title != "Owner" || body.Controller.Address.Country != "US") {
					t.Error("controller defaults lost")
				}
				return response(201, "")
			})
			loc, err := c.CreateCustomer(context.Background(), Customer{Type: kind, FirstName: "Jane", BusinessType: "llc"}, "retry-key")
			if err != nil || loc != "https://api-sandbox.dwolla.com/created-id" {
				t.Fatal(loc, err)
			}
		})
	}
	c := client(t, func(r *http.Request) *http.Response {
		if r.URL.Path != "/customers/c-1" {
			t.Error(r.URL.Path)
		}
		return response(200, `{"ok":true}`)
	})
	if _, err := c.UpdateCustomer(context.Background(), "c-1", CustomerUpdate{Email: "a@example.test"}, "id"); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"deactivated", "reactivated", "suspended"} {
		if _, err := c.SetCustomerStatus(context.Background(), "c-1", status, "id"); err != nil {
			t.Fatal(err)
		}
	}
}
func TestOwnersAndDocuments(t *testing.T) {
	c := client(t, func(r *http.Request) *http.Response {
		if r.Method != "GET" && r.Header.Get("Idempotency-Key") != "id" {
			t.Error("missing idempotency")
		}
		if strings.HasSuffix(r.URL.Path, "/documents") {
			reader, err := r.MultipartReader()
			if err != nil {
				t.Fatal(err)
			}
			p, err := reader.NextPart()
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(p)
			if !bytes.Equal(b, []byte{0, 1, 255}) || p.FileName() != `id".pdf` || p.Header.Get("Content-Type") != "application/pdf" {
				t.Error("document data lost")
			}
			p, err = reader.NextPart()
			if err != nil {
				t.Fatal(err)
			}
			b, _ = io.ReadAll(p)
			if p.FormName() != "documentType" || string(b) != "license" {
				t.Error("document type lost")
			}
			return response(201, "")
		}
		if r.URL.Path == "/customers/c-1/beneficial-owners" {
			return response(201, "")
		}
		if strings.HasSuffix(r.URL.Path, "/beneficial-ownership") {
			return response(200, `{"status":"certified"}`)
		}
		return response(200, `{"firstName":"Jane","address":{"stateProvinceRegion":"CA"},"verificationStatus":"verified"}`)
	})
	ctx := context.Background()
	if _, err := c.CreateOwner(ctx, "c-1", Owner{FirstName: "Jane"}, "id"); err != nil {
		t.Fatal(err)
	}
	if owner, err := c.GetOwner(ctx, "o-1"); err != nil || owner.Address.State != "CA" || owner.VerificationStatus != "verified" {
		t.Fatal(owner, err)
	}
	if _, err := c.UpdateOwner(ctx, "o-1", Owner{FirstName: "Jane"}, "id"); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteOwner(ctx, "o-1", "id"); err != nil {
		t.Fatal(err)
	}
	if err := c.CertifyOwnership(ctx, "c-1", "id"); err != nil {
		t.Fatal(err)
	}
	if status, err := c.OwnershipStatus(ctx, "c-1"); err != nil || status != "certified" {
		t.Fatal(status, err)
	}
	for _, kind := range []string{"customers", "beneficial-owners"} {
		if _, err := c.UploadDocument(ctx, kind, "id-1", `id".pdf`, "application/pdf", "license", []byte{0, 1, 255}, "id"); err != nil {
			t.Fatal(err)
		}
	}
}
func TestErrorStatusAndCredentialRedaction(t *testing.T) {
	c := client(t, func(*http.Request) *http.Response { return response(403, `{"message":"secret token account-data"}`) })
	_, err := c.CreateCustomer(context.Background(), Customer{}, "id")
	var status *StatusError
	if !errors.As(err, &status) || status.Status != 403 || strings.Contains(err.Error(), "secret") {
		t.Fatal(err)
	}
	if _, err = c.GetOwner(context.Background(), "../customers"); err == nil {
		t.Fatal("accepted traversal")
	}
}
