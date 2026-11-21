package nodepush

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client talks to one center node. It is safe for concurrent use:
// the admission checker and the pusher call it from different
// goroutines.
type Client struct {
	base  string // center base URL, no trailing slash
	token string
	node  string
	http  *http.Client
}

// NewClient builds a client for the center at centerURL (e.g.
// "https://center.example:10800"). nodeName is what this worker
// calls itself in ingest batches; the center counts per-node
// ingest under it.
func NewClient(centerURL, token, nodeName string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{
		base:  strings.TrimRight(centerURL, "/"),
		token: token,
		node:  nodeName,
		http:  httpClient,
	}
}

// NodeName returns the name this worker reports to the center.
func (c *Client) NodeName() string { return c.node }

// post sends one JSON body to a node endpoint and decodes the JSON
// response into out (when non-nil). Any non-200 status is an error
// carrying the status and the center's error message.
func (c *Client) post(ctx context.Context, path string, body, out any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(msg, &e) == nil && e.Error != "" {
			return fmt.Errorf("%s: %s: %s", path, resp.Status, e.Error)
		}
		return fmt.Errorf("%s: %s", path, resp.Status)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

// Check asks the center for the admission state of hashes and
// returns the stored and backoff sets. Callers chunk large inputs
// themselves (see Checker); one call carries at most checkMaxHashes
// hashes by protocol.
func (c *Client) Check(ctx context.Context, hashes []string) (stored, backoff map[string]bool, err error) {
	var resp CheckResponse
	if err := c.post(ctx, "/api/node/check", CheckRequest{Hashes: hashes}, &resp); err != nil {
		return nil, nil, err
	}
	stored = make(map[string]bool, len(resp.Stored))
	for _, h := range resp.Stored {
		stored[h] = true
	}
	backoff = make(map[string]bool, len(resp.Backoff))
	for _, h := range resp.Backoff {
		backoff[h] = true
	}
	return stored, backoff, nil
}

// Ingest pushes one batch and returns the center's summary.
func (c *Client) Ingest(ctx context.Context, req IngestRequest) (*IngestResponse, error) {
	req.Node = c.node
	var resp IngestResponse
	if err := c.post(ctx, "/api/node/ingest", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}
