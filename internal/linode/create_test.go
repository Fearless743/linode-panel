package linode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCreateInstance table-drives the POST /linode/instances request shape,
// JSON body field presence and response decoding.
func TestCreateInstance(t *testing.T) {
	trueVal := true
	falseVal := false
	cases := []struct {
		name    string
		opts    CreateOptions
		status  int
		body    string
		want    func(map[string]any) bool
		wantErr bool
	}{
		{
			name: "full options and 200 decode",
			opts: CreateOptions{
				Region: "us-east", Type: "g6-standard-1", Image: "linode/debian12",
				RootPass: "s3cret", AuthorizedKeys: []string{"ssh-ed25519 AAA"}, Label: "web",
				Booted: &trueVal, Tags: []string{"prod"},
			},
			status: http.StatusOK,
			body:   `{"id":7,"label":"web","status":"running","region":"us-east","type":"g6-standard-1","image":"linode/debian12","ipv4":["1.2.3.4"],"ipv6":"2600::1","tags":["prod"],"created":"2024-01-01T00:00:00","updated":"2024-01-01T00:00:00"}`,
			want: func(m map[string]any) bool {
				if m["region"] != "us-east" || m["type"] != "g6-standard-1" || m["image"] != "linode/debian12" {
					return false
				}
				if m["root_pass"] != "s3cret" {
					return false
				}
				keys, ok := m["authorized_keys"].([]any)
				if !ok || len(keys) != 1 || keys[0] != "ssh-ed25519 AAA" {
					return false
				}
				if m["label"] != "web" {
					return false
				}
				if m["booted"] != true {
					return false
				}
				tags, ok := m["tags"].([]any)
				if !ok || len(tags) != 1 || tags[0] != "prod" {
					return false
				}
				return true
			},
		},
		{
			name: "empty optional fields are omitted",
			opts: CreateOptions{
				Region: "us-east", Type: "g6-standard-1", Image: "linode/debian12",
			},
			status: http.StatusOK,
			body:   `{"id":7,"label":"","status":"running","region":"us-east","type":"g6-standard-1","image":"linode/debian12","ipv4":[],"ipv6":"","tags":[],"created":"","updated":""}`,
			want: func(m map[string]any) bool {
				if m["region"] != "us-east" || m["type"] != "g6-standard-1" || m["image"] != "linode/debian12" {
					return false
				}
				for _, k := range []string{"root_pass", "authorized_keys", "label", "booted", "tags"} {
					if _, ok := m[k]; ok {
						return false
					}
				}
				return true
			},
		},
		{
			name: "booted false is serialized",
			opts: CreateOptions{
				Region: "us-east", Type: "g6-standard-1", Image: "linode/debian12",
				Booted: &falseVal,
			},
			status: http.StatusOK,
			body:   `{"id":7,"label":"","status":"running","region":"us-east","type":"g6-standard-1","image":"linode/debian12","ipv4":[],"ipv6":"","tags":[],"created":"","updated":""}`,
			want: func(m map[string]any) bool {
				return m["booted"] == false
			},
		},
		{
			name: "204 with empty body yields zero instance",
			opts: CreateOptions{
				Region: "us-east", Type: "g6-standard-1", Image: "linode/debian12",
			},
			status: http.StatusNoContent,
			want: func(m map[string]any) bool {
				return true
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotMethod, gotPath, gotAuth string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMethod = r.Method
				gotPath = r.URL.Path
				gotAuth = r.Header.Get("Authorization")
				raw, err := io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("read body: %v", err)
				}
				var m map[string]any
				if len(raw) > 0 {
					if err := json.Unmarshal(raw, &m); err != nil {
						t.Errorf("unmarshal body %q: %v", raw, err)
					}
				}
				if !tc.want(m) {
					t.Errorf("unexpected body: %s", raw)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()

			c := New("secret-token")
			c.BaseURL = srv.URL
			inst, err := c.CreateInstance(context.Background(), tc.opts)
			if err != nil {
				t.Fatalf("CreateInstance error: %v", err)
			}
			if gotMethod != http.MethodPost {
				t.Errorf("method = %q, want POST", gotMethod)
			}
			if gotPath != "/linode/instances" {
				t.Errorf("path = %q, want /linode/instances", gotPath)
			}
			if gotAuth != "Bearer secret-token" {
				t.Errorf("Authorization = %q", gotAuth)
			}
			if tc.status == http.StatusOK && inst.ID != 7 {
				t.Errorf("decoded ID = %d, want 7", inst.ID)
			}
			if tc.status == http.StatusNoContent && inst.ID != 0 {
				t.Errorf("expected zero instance on 204, got %+v", inst)
			}
		})
	}
}

// TestCreateInstanceMissingRequired asserts region/type/image are validated
// before any HTTP request is made.
func TestCreateInstanceMissingRequired(t *testing.T) {
	cases := []struct {
		name string
		opts CreateOptions
	}{
		{"missing region", CreateOptions{Type: "g6-standard-1", Image: "linode/debian12"}},
		{"missing type", CreateOptions{Region: "us-east", Image: "linode/debian12"}},
		{"missing image", CreateOptions{Region: "us-east", Type: "g6-standard-1"}},
		{"missing all", CreateOptions{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			}))
			defer srv.Close()

			c := New("t")
			c.BaseURL = srv.URL
			_, err := c.CreateInstance(context.Background(), tc.opts)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), "region, type and image are required") {
				t.Errorf("unexpected error: %v", err)
			}
			if requests != 0 {
				t.Errorf("made %d requests, want 0", requests)
			}
		})
	}
}

// TestDeleteInstance table-drives DELETE /linode/instances/{id} with
// 200 and 204 success responses.
func TestDeleteInstance(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusNoContent} {
		t.Run(fmt.Sprintf("status_%d", status), func(t *testing.T) {
			var gotMethod, gotPath, gotAuth string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMethod = r.Method
				gotPath = r.URL.Path
				gotAuth = r.Header.Get("Authorization")
				w.WriteHeader(status)
			}))
			defer srv.Close()

			c := New("secret-token")
			c.BaseURL = srv.URL
			if err := c.DeleteInstance(context.Background(), 42); err != nil {
				t.Fatalf("DeleteInstance error: %v", err)
			}
			if gotMethod != http.MethodDelete {
				t.Errorf("method = %q, want DELETE", gotMethod)
			}
			if gotPath != "/linode/instances/42" {
				t.Errorf("path = %q, want /linode/instances/42", gotPath)
			}
			if gotAuth != "Bearer secret-token" {
				t.Errorf("Authorization = %q", gotAuth)
			}
		})
	}
}

// TestCreateDeleteAPIError verifies non-2xx responses surface *APIError from
// both CreateInstance and DeleteInstance.
func TestCreateDeleteAPIError(t *testing.T) {
	cases := []struct {
		name string
		call func(*Client) error
	}{
		{"create", func(c *Client) error {
			_, err := c.CreateInstance(context.Background(), CreateOptions{Region: "us-east", Type: "g6-standard-1", Image: "linode/debian12"})
			return err
		}},
		{"delete", func(c *Client) error { return c.DeleteInstance(context.Background(), 42) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, `{"errors":[{"reason":"invalid request"}]}`)
			}))
			defer srv.Close()

			c := New("t")
			c.BaseURL = srv.URL
			err := tc.call(c)
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("want *APIError, got %T: %v", err, err)
			}
			if apiErr.StatusCode != http.StatusBadRequest {
				t.Errorf("StatusCode = %d, want 400", apiErr.StatusCode)
			}
			if apiErr.Reason != "invalid request" {
				t.Errorf("Reason = %q", apiErr.Reason)
			}
		})
	}
}
