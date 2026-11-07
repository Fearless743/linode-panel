package linode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

const (
	defaultBaseURL   = "https://api.linode.com/v4"
	defaultUserAgent = "linode-panel/0.1"
	pageSize         = 500
	maxResponseBytes = 8 << 20 // 8 MiB
)

// Client is a minimal Linode API v4 client.
//
// Token is the bearer token; BaseURL, HTTP and UserAgent default to sane
// values and may be overridden (e.g. in tests or behind a gateway).
type Client struct {
	Token     string
	BaseURL   string
	HTTP      *http.Client
	UserAgent string
}

// New returns a Client configured for the Linode API v4.
func New(token string) *Client {
	return &Client{
		Token:     token,
		BaseURL:   defaultBaseURL,
		HTTP:      &http.Client{Timeout: 30 * time.Second},
		UserAgent: defaultUserAgent,
	}
}

// ListInstances returns all Linode instances, following pagination.
func (c *Client) ListInstances(ctx context.Context) ([]Instance, error) {
	return paginate[Instance](c, ctx, "/linode/instances")
}

// GetInstance returns a single instance.
func (c *Client) GetInstance(ctx context.Context, id int) (Instance, error) {
	var inst Instance
	err := c.do(ctx, http.MethodGet, "/linode/instances/"+strconv.Itoa(id), nil, &inst)
	return inst, err
}

// Boot powers on an instance. A 200 response (empty object) is success.
func (c *Client) Boot(ctx context.Context, id int) error {
	return c.do(ctx, http.MethodPost, "/linode/instances/"+strconv.Itoa(id)+"/boot", nil, nil)
}

// Shutdown powers off an instance.
func (c *Client) Shutdown(ctx context.Context, id int) error {
	return c.do(ctx, http.MethodPost, "/linode/instances/"+strconv.Itoa(id)+"/shutdown", nil, nil)
}

// Reboot restarts an instance.
func (c *Client) Reboot(ctx context.Context, id int) error {
	return c.do(ctx, http.MethodPost, "/linode/instances/"+strconv.Itoa(id)+"/reboot", nil, nil)
}

// UpdateLabel renames an instance (PUT /v4/linode/instances/{id}).
func (c *Client) UpdateLabel(ctx context.Context, id int, label string) error {
	body, err := json.Marshal(map[string]string{"label": label})
	if err != nil {
		return fmt.Errorf("linode api: %w", err)
	}
	return c.do(ctx, http.MethodPut, "/linode/instances/"+strconv.Itoa(id), bytes.NewReader(body), nil)
}

// GetInstanceIPs returns the IPv4/IPv6 addresses of an instance.
func (c *Client) GetInstanceIPs(ctx context.Context, id int) (InstanceIPs, error) {
	var ips InstanceIPs
	err := c.do(ctx, http.MethodGet, "/linode/instances/"+strconv.Itoa(id)+"/ips", nil, &ips)
	return ips, err
}

// do performs one request, decoding a successful JSON response into out.
// out may be nil for actions with no response body (POST boot/shutdown/reboot).
func (c *Client) do(ctx context.Context, method, path string, body io.Reader, out any) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("linode api: %w", err)
	}
	if c.HTTP == nil {
		return fmt.Errorf("linode api: nil HTTP client")
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return fmt.Errorf("linode api: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("linode api: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("linode api: %w", err)
	}
	if len(raw) > maxResponseBytes {
		return fmt.Errorf("linode api: response exceeds %d bytes", maxResponseBytes)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{StatusCode: resp.StatusCode, Reason: reasonFromBody(raw, resp.StatusCode)}
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("linode api: %w", err)
		}
	}
	return nil
}

// reasonFromBody extracts errors[0].reason, falling back to the HTTP status text.
func reasonFromBody(raw []byte, status int) string {
	var payload struct {
		Errors []struct {
			Reason string `json:"reason"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &payload); err == nil && len(payload.Errors) > 0 && payload.Errors[0].Reason != "" {
		return payload.Errors[0].Reason
	}
	return http.StatusText(status)
}

// pageResponse is one page of a paginated collection.
type pageResponse[T any] struct {
	Data  []T `json:"data"`
	Page  int `json:"page"`
	Pages int `json:"pages"`
}

// paginate follows {data,page,pages} until page >= pages (empty pages = 1).
func paginate[T any](c *Client, ctx context.Context, path string) ([]T, error) {
	var all []T
	for page := 1; ; page++ {
		url := path + "?page_size=" + strconv.Itoa(pageSize) + "&page=" + strconv.Itoa(page)
		var resp pageResponse[T]
		if err := c.do(ctx, http.MethodGet, url, nil, &resp); err != nil {
			return nil, err
		}
		all = append(all, resp.Data...)
		pages := resp.Pages
		if pages <= 0 {
			pages = 1
		}
		if page >= pages || len(resp.Data) == 0 {
			return all, nil
		}
	}
}
