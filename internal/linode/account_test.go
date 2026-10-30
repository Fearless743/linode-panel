package linode

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestClient_GetAccount checks request shape and successful body decoding.
func TestClient_GetAccount(t *testing.T) {
	var gotMethod, gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotAuth = r.Method, r.URL.Path, r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		// Extra fields (e.g. first_name) are ignored by the struct.
		_, _ = w.Write([]byte(`{"balance":12.5,"balance_uninvoiced":3.25,"company":"Acme","email":"ops@example.com","first_name":"Ada"}`))
	}))
	defer srv.Close()

	c := New("secret-token")
	c.BaseURL = srv.URL
	acct, err := c.GetAccount(context.Background())
	if err != nil {
		t.Fatalf("GetAccount error: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if gotPath != "/account" {
		t.Errorf("path = %q, want /account", gotPath)
	}
	if gotAuth != "Bearer secret-token" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if acct.Balance != 12.5 {
		t.Errorf("Balance = %v, want 12.5", acct.Balance)
	}
	if acct.BalanceUninvoiced != 3.25 {
		t.Errorf("BalanceUninvoiced = %v, want 3.25", acct.BalanceUninvoiced)
	}
	if acct.Company != "Acme" {
		t.Errorf("Company = %q, want Acme", acct.Company)
	}
	if acct.Email != "ops@example.com" {
		t.Errorf("Email = %q, want ops@example.com", acct.Email)
	}
}

// TestClient_GetAccountForbidden verifies a 403 yields *APIError with the reason.
func TestClient_GetAccountForbidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"errors":[{"reason":"Account access is restricted"}]}`))
	}))
	defer srv.Close()

	c := New("t")
	c.BaseURL = srv.URL
	_, err := c.GetAccount(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("want *APIError, got %T: %v", err, err)
	}
	if apiErr.StatusCode != http.StatusForbidden {
		t.Errorf("StatusCode = %d, want 403", apiErr.StatusCode)
	}
	if apiErr.Reason != "Account access is restricted" {
		t.Errorf("Reason = %q", apiErr.Reason)
	}
}

// TestClient_GetAccountNetworkError verifies a transport failure is wrapped.
func TestClient_GetAccountNetworkError(t *testing.T) {
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	})

	c := New("t")
	c.BaseURL = "https://api.example.invalid/v4"
	c.HTTP = &http.Client{Transport: rt}
	_, err := c.GetAccount(context.Background())
	if err == nil {
		t.Fatal("want network error, got nil")
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("error = %v, want wrapped network error", err)
	}
}

// TestClient_GetAccountDecodeError verifies malformed JSON surfaces an error.
func TestClient_GetAccountDecodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"balance":`))
	}))
	defer srv.Close()

	c := New("t")
	c.BaseURL = srv.URL
	if _, err := c.GetAccount(context.Background()); err == nil {
		t.Fatal("want decode error, got nil")
	}
}
