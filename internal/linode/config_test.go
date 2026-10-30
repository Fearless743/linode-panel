package linode

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestClient_GetConfigID checks the request shape and the first-config lookup.
func TestClient_GetConfigID(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		// Linode returns {data:[...]}; the panel only needs the first id.
		_, _ = w.Write([]byte(`{"data":[{"id":7,"label":"my-config"}]}`))
	}))
	defer srv.Close()

	c := New("secret-token")
	c.BaseURL = srv.URL
	id, err := c.GetConfigID(context.Background(), 42)
	if err != nil {
		t.Fatalf("GetConfigID error: %v", err)
	}
	if gotPath != "/linode/instances/42/configs" {
		t.Errorf("path = %q, want /linode/instances/42/configs", gotPath)
	}
	if id != 7 {
		t.Errorf("id = %d, want 7", id)
	}
}

// TestClient_GetConfigIDEmpty verifies an instance with no configs errors.
func TestClient_GetConfigIDEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	c := New("secret-token")
	c.BaseURL = srv.URL
	if _, err := c.GetConfigID(context.Background(), 42); err == nil {
		t.Fatalf("expected error for empty configs")
	}
}

// TestClient_UpdateConfigNetworkHelper checks the PATCH body.
func TestClient_UpdateConfigNetworkHelper(t *testing.T) {
	var gotMethod, gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		gotBody = string(body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New("secret-token")
	c.BaseURL = srv.URL
	err := c.UpdateConfigNetworkHelper(context.Background(), 42, 7, true)
	if err != nil {
		t.Fatalf("UpdateConfigNetworkHelper error: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("method = %q, want PATCH", gotMethod)
	}
	if gotPath != "/linode/instances/42/configs/7" {
		t.Errorf("path = %q, want /linode/instances/42/configs/7", gotPath)
	}
	if !strings.Contains(gotBody, `"network":true`) {
		t.Errorf("body = %q, want network:true", gotBody)
	}
}

// TestClient_AssignIP checks the POST /networking/ips/assign body.
func TestClient_AssignIP(t *testing.T) {
	var gotMethod, gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		gotBody = string(body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New("secret-token")
	c.BaseURL = srv.URL
	err := c.AssignIP(context.Background(), "us-east", "192.0.2.10", 99)
	if err != nil {
		t.Fatalf("AssignIP error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/networking/ips/assign" {
		t.Errorf("path = %q, want /networking/ips/assign", gotPath)
	}
	if !strings.Contains(gotBody, `"region":"us-east"`) || !strings.Contains(gotBody, `"address":"192.0.2.10"`) || !strings.Contains(gotBody, `"linode_id":99`) {
		t.Errorf("body = %q, want region/address/linode_id", gotBody)
	}
}
