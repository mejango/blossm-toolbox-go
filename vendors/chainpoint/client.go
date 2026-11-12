// Package chainpoint submits hashes and retrieves opaque proofs from a configured gateway.
package chainpoint

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"

	"github.com/mejango/blossm-toolbox-go/vendors/internal/httpwire"
)

type Client struct{ wire *httpwire.Client }
type Handle struct {
	ID  string `json:"id"`
	URI string `json:"uri"`
}
type Proof struct {
	Value           json.RawMessage `json:"value"`
	AnchorsComplete []string        `json:"anchorsComplete"`
}

var proofID = regexp.MustCompile(`^[A-Za-z0-9-]{16,64}$`)

func New(gateway string, h *http.Client) (*Client, error) {
	w, err := httpwire.New(gateway, h)
	if err != nil {
		return nil, err
	}
	return &Client{w}, nil
}
func (c *Client) SubmitHash(ctx context.Context, hash string) ([]Handle, error) {
	if len(hash) < 40 || len(hash) > 128 {
		return nil, errors.New("hash must be 20-64 bytes encoded as hex")
	}
	if _, err := hex.DecodeString(hash); err != nil {
		return nil, errors.New("invalid hexadecimal hash")
	}
	b, _ := json.Marshal(map[string]any{"hashes": []string{hash}})
	body, _, err := c.wire.Do(ctx, "POST", "/hashes", http.Header{"Content-Type": {"application/json"}, "Accept": {"application/json"}}, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	var response struct {
		Hashes []struct {
			Hash string `json:"hash"`
			ID   string `json:"proof_id"`
		} `json:"hashes"`
	}
	if err = json.Unmarshal(body, &response); err != nil {
		return nil, errors.New("invalid hash submission response")
	}
	handles := []Handle{}
	for _, h := range response.Hashes {
		if h.Hash == hash {
			if !proofID.MatchString(h.ID) {
				return nil, errors.New("invalid proof identifier")
			}
			handles = append(handles, Handle{h.ID, c.wire.Endpoint()})
		}
	}
	if len(handles) == 0 {
		return nil, errors.New("gateway did not acknowledge submitted hash")
	}
	return handles, nil
}
func (c *Client) GetProof(ctx context.Context, handle Handle) (*Proof, error) {
	if handle.URI != c.wire.Endpoint() || !proofID.MatchString(handle.ID) {
		return nil, errors.New("proof handle does not belong to configured gateway")
	}
	body, _, err := c.wire.Do(ctx, "GET", "/proofs", http.Header{"Accept": {"application/json"}, "Proofids": {handle.ID}}, nil)
	if err != nil {
		return nil, err
	}
	var results []struct {
		ID      string          `json:"proof_id"`
		Proof   json.RawMessage `json:"proof"`
		Anchors []string        `json:"anchors_complete"`
	}
	if err = json.Unmarshal(body, &results); err != nil {
		return nil, errors.New("invalid proof response")
	}
	for _, r := range results {
		if r.ID == handle.ID {
			return &Proof{r.Proof, r.Anchors}, nil
		}
	}
	return nil, nil
}
