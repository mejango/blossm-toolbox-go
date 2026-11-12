// Package dwolla implements the retained customer and beneficial-owner API surface.
package dwolla

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"regexp"
	"strings"

	"github.com/mejango/blossm-toolbox-go/integrations/internal/httpwire"
)

const media = "application/vnd.dwolla.v1.hal+json"

type Client struct {
	wire        *httpwire.Client
	key, secret string
}
type StatusError = httpwire.StatusError

func New(endpoint, key, secret string, h *http.Client) (*Client, error) {
	if key == "" || secret == "" {
		return nil, errors.New("Dwolla key and secret are required")
	}
	w, err := httpwire.New(endpoint, h)
	if err != nil {
		return nil, err
	}
	return &Client{w, key, secret}, nil
}
func (c *Client) headers(ctx context.Context, idempotency string) (http.Header, error) {
	h := http.Header{"Content-Type": {"application/x-www-form-urlencoded"}, "Authorization": {"Basic " + base64.StdEncoding.EncodeToString([]byte(c.key+":"+c.secret))}}
	b, _, err := c.wire.Do(ctx, "POST", "/token", h, strings.NewReader("grant_type=client_credentials"))
	if err != nil {
		return nil, err
	}
	var token struct {
		Token   string `json:"access_token"`
		Type    string `json:"token_type"`
		Expires int    `json:"expires_in"`
	}
	if err = json.Unmarshal(b, &token); err != nil || token.Token == "" || !strings.EqualFold(token.Type, "Bearer") || token.Expires <= 0 {
		return nil, errors.New("invalid Dwolla access token response")
	}
	h = http.Header{"Authorization": {"Bearer " + token.Token}, "Accept": {media}, "Content-Type": {media}}
	if idempotency != "" {
		h.Set("Idempotency-Key", idempotency)
	}
	return h, nil
}
func (c *Client) json(ctx context.Context, method, path string, data any, key string) (json.RawMessage, http.Header, error) {
	var body io.Reader
	if data != nil {
		b, err := json.Marshal(data)
		if err != nil {
			return nil, nil, err
		}
		body = bytes.NewReader(b)
	}
	h, err := c.headers(ctx, key)
	if err != nil {
		return nil, nil, err
	}
	b, headers, err := c.wire.Do(ctx, method, path, h, body)
	if err != nil {
		return nil, nil, err
	}
	if len(b) > 0 && !json.Valid(b) {
		return nil, nil, errors.New("invalid Dwolla JSON response")
	}
	return b, headers, nil
}

var identifier = regexp.MustCompile(`^[A-Za-z0-9-]+$`)

func path(kind, id, suffix string) (string, error) {
	if !identifier.MatchString(id) {
		return "", errors.New("invalid Dwolla resource identifier")
	}
	return "/" + kind + "/" + id + suffix, nil
}
func location(h http.Header) (string, error) {
	if h.Get("Location") == "" {
		return "", errors.New("Dwolla create response is missing Location")
	}
	return h.Get("Location"), nil
}

type Address struct {
	Address1   string `json:"address1"`
	Address2   string `json:"address2,omitempty"`
	City       string `json:"city"`
	State      string `json:"stateProvinceRegion"`
	Country    string `json:"country"`
	PostalCode string `json:"postalCode"`
}
type Passport struct {
	Number  string `json:"number"`
	Country string `json:"country"`
}
type Owner struct {
	FirstName          string    `json:"firstName"`
	LastName           string    `json:"lastName"`
	SSN                string    `json:"ssn,omitempty"`
	DateOfBirth        string    `json:"dateOfBirth,omitempty"`
	Address            Address   `json:"address"`
	Passport           *Passport `json:"passport,omitempty"`
	VerificationStatus string    `json:"verificationStatus,omitempty"`
}
type Controller struct {
	FirstName   string    `json:"firstName"`
	LastName    string    `json:"lastName"`
	Title       string    `json:"title"`
	SSN         string    `json:"ssn,omitempty"`
	DateOfBirth string    `json:"dateOfBirth"`
	Address     Address   `json:"address"`
	Passport    *Passport `json:"passport,omitempty"`
}
type Customer struct {
	FirstName              string      `json:"firstName"`
	LastName               string      `json:"lastName"`
	Email                  string      `json:"email"`
	Type                   string      `json:"type,omitempty"`
	IPAddress              string      `json:"ipAddress,omitempty"`
	DateOfBirth            string      `json:"dateOfBirth,omitempty"`
	SSN                    string      `json:"ssn,omitempty"`
	Address1               string      `json:"address1,omitempty"`
	Address2               string      `json:"address2,omitempty"`
	City                   string      `json:"city,omitempty"`
	State                  string      `json:"state,omitempty"`
	PostalCode             string      `json:"postalCode,omitempty"`
	BusinessName           string      `json:"businessName,omitempty"`
	BusinessType           string      `json:"businessType,omitempty"`
	BusinessClassification string      `json:"businessClassification,omitempty"`
	EIN                    string      `json:"ein,omitempty"`
	Website                string      `json:"website,omitempty"`
	Phone                  string      `json:"phone,omitempty"`
	CorrelationID          string      `json:"correlationId,omitempty"`
	Controller             *Controller `json:"controller,omitempty"`
}

func (c *Client) CreateCustomer(ctx context.Context, v Customer, key string) (string, error) {
	switch v.Type {
	case "", "receive-only", "personal", "business":
	default:
		return "", errors.New("unsupported customer type")
	}
	if v.Type == "business" && (v.BusinessType == "llc" || v.BusinessType == "corporation" || v.BusinessType == "partnership") {
		controller := Controller{}
		if v.Controller != nil {
			controller = *v.Controller
		}
		fill := func(s *string, f string) {
			if *s == "" {
				*s = f
			}
		}
		fill(&controller.FirstName, v.FirstName)
		fill(&controller.LastName, v.LastName)
		fill(&controller.Title, "Owner")
		fill(&controller.SSN, v.SSN)
		fill(&controller.DateOfBirth, v.DateOfBirth)
		fill(&controller.Address.Address1, v.Address1)
		fill(&controller.Address.Address2, v.Address2)
		fill(&controller.Address.City, v.City)
		fill(&controller.Address.State, v.State)
		fill(&controller.Address.Country, "US")
		fill(&controller.Address.PostalCode, v.PostalCode)
		v.Controller = &controller
	}
	_, h, err := c.json(ctx, "POST", "/customers", v, key)
	if err != nil {
		return "", err
	}
	return location(h)
}

type CustomerUpdate struct {
	FirstName    string `json:"firstName,omitempty"`
	LastName     string `json:"lastName,omitempty"`
	Email        string `json:"email,omitempty"`
	BusinessName string `json:"businessName,omitempty"`
	IPAddress    string `json:"ipAddress,omitempty"`
	Address1     string `json:"address1,omitempty"`
	Address2     string `json:"address2,omitempty"`
	City         string `json:"city,omitempty"`
	State        string `json:"state,omitempty"`
	PostalCode   string `json:"postalCode,omitempty"`
	Website      string `json:"website,omitempty"`
	Phone        string `json:"phone,omitempty"`
}

func (c *Client) UpdateCustomer(ctx context.Context, id string, v CustomerUpdate, key string) (json.RawMessage, error) {
	p, err := path("customers", id, "")
	if err != nil {
		return nil, err
	}
	b, _, err := c.json(ctx, "POST", p, v, key)
	return b, err
}
func (c *Client) SetCustomerStatus(ctx context.Context, id, status, key string) (json.RawMessage, error) {
	switch status {
	case "deactivated", "reactivated", "suspended":
	default:
		return nil, errors.New("invalid customer status")
	}
	p, err := path("customers", id, "")
	if err != nil {
		return nil, err
	}
	b, _, err := c.json(ctx, "POST", p, map[string]string{"status": status}, key)
	return b, err
}
func (c *Client) CreateOwner(ctx context.Context, customerID string, v Owner, key string) (string, error) {
	p, err := path("customers", customerID, "/beneficial-owners")
	if err != nil {
		return "", err
	}
	v.VerificationStatus = ""
	_, h, err := c.json(ctx, "POST", p, v, key)
	if err != nil {
		return "", err
	}
	return location(h)
}
func (c *Client) GetOwner(ctx context.Context, id string) (Owner, error) {
	return c.owner(ctx, "GET", id, nil, "")
}
func (c *Client) UpdateOwner(ctx context.Context, id string, v Owner, key string) (Owner, error) {
	v.VerificationStatus = ""
	return c.owner(ctx, "POST", id, v, key)
}
func (c *Client) owner(ctx context.Context, method, id string, v any, key string) (Owner, error) {
	var result Owner
	p, err := path("beneficial-owners", id, "")
	if err != nil {
		return result, err
	}
	b, _, err := c.json(ctx, method, p, v, key)
	if err != nil {
		return result, err
	}
	if err = json.Unmarshal(b, &result); err != nil {
		return result, errors.New("invalid beneficial-owner response")
	}
	return result, nil
}
func (c *Client) DeleteOwner(ctx context.Context, id, key string) error {
	p, err := path("beneficial-owners", id, "")
	if err != nil {
		return err
	}
	_, _, err = c.json(ctx, "DELETE", p, nil, key)
	return err
}
func (c *Client) CertifyOwnership(ctx context.Context, id, key string) error {
	p, err := path("customers", id, "/beneficial-ownership")
	if err != nil {
		return err
	}
	_, _, err = c.json(ctx, "POST", p, map[string]string{"status": "certified"}, key)
	return err
}
func (c *Client) OwnershipStatus(ctx context.Context, id string) (string, error) {
	p, err := path("customers", id, "/beneficial-ownership")
	if err != nil {
		return "", err
	}
	b, _, err := c.json(ctx, "GET", p, nil, "")
	if err != nil {
		return "", err
	}
	var v struct {
		Status string `json:"status"`
	}
	if err = json.Unmarshal(b, &v); err != nil {
		return "", errors.New("invalid ownership status response")
	}
	return v.Status, nil
}

// UploadDocument supports both customer and beneficial-owner documents, preserving bytes and MIME.
func (c *Client) UploadDocument(ctx context.Context, kind, id, filename, contentType, documentType string, data []byte, key string) (string, error) {
	if kind != "customers" && kind != "beneficial-owners" {
		return "", errors.New("document owner must be customers or beneficial-owners")
	}
	if len(data) > 10<<20 {
		return "", errors.New("document exceeds 10 MiB")
	}
	if _, _, err := mime.ParseMediaType(contentType); err != nil {
		return "", errors.New("invalid document content type")
	}
	if strings.ContainsAny(contentType, "\r\n") || filename == "" {
		return "", errors.New("invalid document metadata")
	}
	p, err := path(kind, id, "/documents")
	if err != nil {
		return "", err
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreatePart(textproto.MIMEHeader{"Content-Disposition": {mime.FormatMediaType("form-data", map[string]string{"name": "file", "filename": filename})}, "Content-Type": {contentType}})
	if err != nil {
		return "", err
	}
	if _, err = part.Write(data); err != nil {
		return "", err
	}
	if err = writer.WriteField("documentType", documentType); err != nil {
		return "", err
	}
	if err = writer.Close(); err != nil {
		return "", err
	}
	h, err := c.headers(ctx, key)
	if err != nil {
		return "", err
	}
	h.Set("Content-Type", writer.FormDataContentType())
	_, headers, err := c.wire.Do(ctx, "POST", p, h, &body)
	if err != nil {
		return "", err
	}
	return location(headers)
}

// Redacted returns a useful printable description without credentials.
func (c *Client) String() string { return fmt.Sprintf("Dwolla(%s)", c.wire.Endpoint()) }
