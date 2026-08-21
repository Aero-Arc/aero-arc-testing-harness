package toxiproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client controls one Toxiproxy server through its HTTP API.
type Client struct {
	baseURL string
	http    *http.Client
}

// Toxic is a Toxiproxy fault attached to one proxy stream.
type Toxic struct {
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Stream     string         `json:"stream"`
	Toxicity   float64        `json:"toxicity"`
	Attributes map[string]any `json:"attributes"`
}

// NewClient returns a bounded Toxiproxy control client.
func NewClient(baseURL string) (*Client, error) {
	parsed, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid Toxiproxy URL %q", baseURL)
	}
	return &Client{baseURL: parsed.String(), http: &http.Client{Timeout: 5 * time.Second}}, nil
}

// CreateProxy creates or replaces a named proxy.
func (client *Client) CreateProxy(ctx context.Context, name, listen, upstream string) error {
	if name == "" || listen == "" || upstream == "" {
		return fmt.Errorf("proxy name, listen, and upstream are required")
	}
	_ = client.DeleteProxy(ctx, name)
	return client.request(ctx, http.MethodPost, "/proxies", map[string]any{
		"name": name, "listen": listen, "upstream": upstream, "enabled": true,
	}, http.StatusCreated, http.StatusOK)
}

// AddToxic attaches a named toxic to a proxy.
func (client *Client) AddToxic(ctx context.Context, proxy string, toxic Toxic) error {
	if toxic.Toxicity == 0 {
		toxic.Toxicity = 1
	}
	if toxic.Stream == "" {
		toxic.Stream = "downstream"
	}
	path := "/proxies/" + url.PathEscape(proxy) + "/toxics"
	return client.request(ctx, http.MethodPost, path, toxic, http.StatusOK, http.StatusCreated)
}

// RemoveToxic removes one toxic and is idempotent for cleanup.
func (client *Client) RemoveToxic(ctx context.Context, proxy, name string) error {
	path := "/proxies/" + url.PathEscape(proxy) + "/toxics/" + url.PathEscape(name)
	return client.request(ctx, http.MethodDelete, path, nil, http.StatusNoContent, http.StatusNotFound)
}

// DeleteProxy removes one proxy and is idempotent for cleanup.
func (client *Client) DeleteProxy(ctx context.Context, name string) error {
	return client.request(ctx, http.MethodDelete, "/proxies/"+url.PathEscape(name), nil, http.StatusNoContent, http.StatusNotFound)
}

func (client *Client) request(ctx context.Context, method, path string, body any, accepted ...int) error {
	var reader io.Reader
	if body != nil {
		var payload bytes.Buffer
		if err := json.NewEncoder(&payload).Encode(body); err != nil {
			return fmt.Errorf("encode Toxiproxy request: %w", err)
		}
		reader = &payload
	}
	request, err := http.NewRequestWithContext(ctx, method, client.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("create Toxiproxy request: %w", err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.http.Do(request)
	if err != nil {
		return fmt.Errorf("Toxiproxy %s %s: %w", method, path, err)
	}
	defer response.Body.Close()
	for _, status := range accepted {
		if response.StatusCode == status {
			return nil
		}
	}
	payload, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	return fmt.Errorf("Toxiproxy %s %s returned %d: %s", method, path, response.StatusCode, strings.TrimSpace(string(payload)))
}
