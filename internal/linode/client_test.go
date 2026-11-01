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
	"sync/atomic"
	"testing"
)

// TestClient_ListInstances checks the request shape and body decoding.
func TestClient_ListInstances(t *testing.T) {
	var gotMethod, gotPath, gotAuth, gotAccept, gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":1,"label":"web","status":"running","region":"us-east","type":"g6-standard-1","ipv4":["1.2.3.4"],"ipv6":"2600::1","tags":["prod"],"created":"2024-01-01T00:00:00"}],"page":1,"pages":1}`)
	}))
	defer srv.Close()

	c := New("secret-token")
	c.BaseURL = srv.URL
	insts, err := c.ListInstances(context.Background())
	if err != nil {
		t.Fatalf("ListInstances error: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if gotPath != "/linode/instances" {
		t.Errorf("path = %q, want /linode/instances", gotPath)
	}
	if gotAuth != "Bearer secret-token" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotAccept != "application/json" {
		t.Errorf("Accept = %q", gotAccept)
	}
	if gotUA != "linode-panel/0.1" {
		t.Errorf("User-Agent = %q", gotUA)
	}
	if len(insts) != 1 {
		t.Fatalf("got %d instances, want 1", len(insts))
	}
	got := insts[0]
	if got.ID != 1 || got.Label != "web" || got.Status != "running" || got.Region != "us-east" ||
		got.Type != "g6-standard-1" || got.IPv6 != "2600::1" || got.Created != "2024-01-01T00:00:00" {
		t.Errorf("unexpected instance: %+v", got)
	}
	if len(got.IPv4) != 1 || got.IPv4[0] != "1.2.3.4" {
		t.Errorf("unexpected ipv4: %v", got.IPv4)
	}
	if len(got.Tags) != 1 || got.Tags[0] != "prod" {
		t.Errorf("unexpected tags: %v", got.Tags)
	}
}

// TestClient_ListInstancesPagination asserts page_size=500, follows pages,
// and merges results across two pages.
func TestClient_ListInstancesPagination(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if got := r.URL.Query().Get("page_size"); got != "500" {
			t.Errorf("page_size = %q, want 500", got)
		}
		page := r.URL.Query().Get("page")
		w.Header().Set("Content-Type", "application/json")
		switch page {
		case "1":
			fmt.Fprint(w, `{"data":[{"id":1,"label":"a"},{"id":2,"label":"b"}],"page":1,"pages":2}`)
		case "2":
			fmt.Fprint(w, `{"data":[{"id":3,"label":"c"}],"page":2,"pages":2}`)
		default:
			t.Errorf("unexpected page %q (call %d)", page, n)
		}
	}))
	defer srv.Close()

	c := New("t")
	c.BaseURL = srv.URL
	insts, err := c.ListInstances(context.Background())
	if err != nil {
		t.Fatalf("ListInstances error: %v", err)
	}
	if calls.Load() != 2 {
		t.Errorf("expected 2 requests, got %d", calls.Load())
	}
	if len(insts) != 3 {
		t.Fatalf("got %d instances, want 3 (merged)", len(insts))
	}
	if insts[0].ID != 1 || insts[1].ID != 2 || insts[2].ID != 3 {
		t.Errorf("unexpected merged order: %+v", insts)
	}
}

// TestClient_GetInstance checks GET /linode/instances/{id}.
func TestClient_GetInstance(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/linode/instances/42" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":42,"label":"db","status":"running"}`)
	}))
	defer srv.Close()

	c := New("t")
	c.BaseURL = srv.URL
	inst, err := c.GetInstance(context.Background(), 42)
	if err != nil {
		t.Fatalf("GetInstance error: %v", err)
	}
	if inst.ID != 42 || inst.Label != "db" || inst.Status != "running" {
		t.Errorf("unexpected instance: %+v", inst)
	}
}

// TestClient_GetInstanceIPs uses the real response shape (object arrays).
func TestClient_GetInstanceIPs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/linode/instances/7/ips" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ipv4":{"public":[{"address":"1.2.3.4","prefix":24,"public":true,"rdns":"web.example.com"}],"private":[{"address":"10.0.0.1","prefix":24,"public":false,"rdns":""}],"reserved":[],"shared":[]},"ipv6":{"slaac":{"address":"2600:3c01::1","prefix":64,"public":true,"rdns":""},"link_local":{"address":"fe80::1","prefix":64,"public":false,"rdns":""}}}`)
	}))
	defer srv.Close()

	c := New("t")
	c.BaseURL = srv.URL
	ips, err := c.GetInstanceIPs(context.Background(), 7)
	if err != nil {
		t.Fatalf("GetInstanceIPs error: %v", err)
	}
	if got := ips.PublicIPv4(); got != "1.2.3.4" {
		t.Errorf("PublicIPv4 = %q, want 1.2.3.4", got)
	}
	if got := ips.SLAACv6(); got != "2600:3c01::1" {
		t.Errorf("SLAACv6 = %q, want 2600:3c01::1", got)
	}
	if len(ips.IPv4.Public) != 1 || ips.IPv4.Public[0].Prefix != 24 || !ips.IPv4.Public[0].Public ||
		ips.IPv4.Public[0].RDNS != "web.example.com" {
		t.Errorf("unexpected public ipv4: %+v", ips.IPv4.Public)
	}
	if len(ips.IPv4.Private) != 1 || ips.IPv4.Private[0].Address != "10.0.0.1" {
		t.Errorf("unexpected private ipv4: %+v", ips.IPv4.Private)
	}
	if ips.IPv6.LinkLocal.Address != "fe80::1" {
		t.Errorf("unexpected link_local: %+v", ips.IPv6.LinkLocal)
	}
}

// TestClient_ActionMethods table-drives boot/shutdown/reboot.
func TestClient_ActionMethods(t *testing.T) {
	cases := []struct {
		name string
		call func(*Client) error
		path string
	}{
		{"boot", func(c *Client) error { return c.Boot(context.Background(), 1) }, "/linode/instances/1/boot"},
		{"shutdown", func(c *Client) error { return c.Shutdown(context.Background(), 1) }, "/linode/instances/1/shutdown"},
		{"reboot", func(c *Client) error { return c.Reboot(context.Background(), 1) }, "/linode/instances/1/reboot"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != tc.path {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				if got := r.Header.Get("Authorization"); got != "Bearer t" {
					t.Errorf("Authorization = %q", got)
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{}`)
			}))
			defer srv.Close()

			c := New("t")
			c.BaseURL = srv.URL
			if err := tc.call(c); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// TestClient_BootError verifies a 400 with errors[0].reason yields APIError.
func TestClient_BootError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"errors":[{"reason":"Linode is already booted"}]}`)
	}))
	defer srv.Close()

	c := New("t")
	c.BaseURL = srv.URL
	err := c.Boot(context.Background(), 1)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("want *APIError, got %T: %v", err, err)
	}
	if apiErr.StatusCode != 400 {
		t.Errorf("StatusCode = %d, want 400", apiErr.StatusCode)
	}
	if apiErr.Reason != "Linode is already booted" {
		t.Errorf("Reason = %q", apiErr.Reason)
	}
}

// TestClient_ErrorStatus drives status codes and reason extraction.
func TestClient_ErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"errors":[{"reason":"Invalid token"}]}`)
	}))
	defer srv.Close()

	c := New("t")
	c.BaseURL = srv.URL
	_, err := c.ListInstances(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("want *APIError, got %T: %v", err, err)
	}
	if apiErr.StatusCode != 401 {
		t.Errorf("StatusCode = %d, want 401", apiErr.StatusCode)
	}
	if apiErr.Reason != "Invalid token" {
		t.Errorf("Reason = %q, want Invalid token", apiErr.Reason)
	}
}

// TestClient_ErrorFallbackReason covers non-JSON error bodies.
func TestClient_ErrorFallbackReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprint(w, `upstream broke`)
	}))
	defer srv.Close()

	c := New("t")
	c.BaseURL = srv.URL
	_, err := c.ListInstances(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("want *APIError, got %T: %v", err, err)
	}
	if apiErr.StatusCode != 502 {
		t.Errorf("StatusCode = %d, want 502", apiErr.StatusCode)
	}
	if apiErr.Reason != http.StatusText(http.StatusBadGateway) {
		t.Errorf("Reason = %q, want %q", apiErr.Reason, http.StatusText(http.StatusBadGateway))
	}
}

// TestClient_EmptyPages treats a missing/zero pages field as a single page.
func TestClient_EmptyPages(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":5,"label":"x"}],"page":1,"pages":0}`)
	}))
	defer srv.Close()

	c := New("t")
	c.BaseURL = srv.URL
	insts, err := c.ListInstances(context.Background())
	if err != nil {
		t.Fatalf("ListInstances error: %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("expected 1 request, got %d", calls.Load())
	}
	if len(insts) != 1 {
		t.Errorf("got %d instances, want 1", len(insts))
	}
}

// TestClient_ContextCancelled verifies a cancelled context is rejected.
func TestClient_ContextCancelled(t *testing.T) {
	c := New("t")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.ListInstances(ctx)
	if err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Errorf("want context canceled error, got %v", err)
	}
}

// TestClient_EmptyUserAgent checks the custom agent is not set when UserAgent is "".
func TestClient_EmptyUserAgent(t *testing.T) {
	var gotUA string
	// Inspect the request at the transport layer, before Go's http.Client
	// injects the default "Go-http-client/1.1" User-Agent.
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		gotUA = r.Header.Get("User-Agent")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"data":[],"page":1,"pages":1}`)),
			Request:    r,
		}, nil
	})

	c := New("t")
	c.BaseURL = "https://api.example.invalid/v4"
	c.UserAgent = ""
	c.HTTP = &http.Client{Transport: rt}
	if _, err := c.ListInstances(context.Background()); err != nil {
		t.Fatalf("ListInstances error: %v", err)
	}
	if gotUA != "" {
		t.Errorf("User-Agent = %q, want unset", gotUA)
	}
}

// roundTripFunc adapts a function to http.RoundTripper.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// --- Cloud Firewall (allow-all) tests ---

// TestClient_CreateFirewall checks the POST body (allow-all rules) and response.
func TestClient_CreateFirewall(t *testing.T) {
	var gotMethod, gotPath, gotAuth string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotAuth = r.Method, r.URL.Path, r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":77,"label":"linode-panel-allow-all","region":"us-east","status":"enabled","tags":["linode-panel-allow-all"]}`)
	}))
	defer srv.Close()

	c := New("t")
	c.BaseURL = srv.URL
	fw, err := c.CreateFirewall(context.Background(), "us-east")
	if err != nil {
		t.Fatalf("CreateFirewall error: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/networking/firewalls" {
		t.Errorf("request = %s %s", gotMethod, gotPath)
	}
	if gotAuth != "Bearer t" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotBody["region"] != "us-east" || gotBody["label"] != allowAllFirewallLabel("us-east") {
		t.Errorf("body = %v", gotBody)
	}
	// The label must be region-scoped: a fixed label collides as soon as a
	// second region creates its own allow-all firewall.
	if allowAllFirewallLabel("us-east") == allowAllFirewallLabel("ap-northeast") {
		t.Error("firewall label is not region-scoped")
	}
	if allowAllFirewallLabel("us-east") == AllowAllFirewallLabel {
		t.Error("firewall label must differ from the legacy region-less label")
	}
	// The label must also fit the API's 3-32 character limit for the longest
	// region ids (Regions like "us-southeast" pushed a longer prefix over it).
	for _, region := range []string{"us-east", "us-southeast", "ap-northeast", "ca-central"} {
		if l := allowAllFirewallLabel(region); len(l) < 3 || len(l) > 32 {
			t.Errorf("allowAllFirewallLabel(%q) = %q (%d chars), want 3-32", region, l, len(l))
		}
	}
	tags, _ := gotBody["tags"].([]any)
	if len(tags) != 1 || tags[0] != AllowAllFirewallTag {
		t.Errorf("tags = %v", gotBody["tags"])
	}
	rules, _ := gotBody["rules"].(map[string]any)
	if rules["inbound_policy"] != "ACCEPT" || rules["outbound_policy"] != "ACCEPT" {
		t.Errorf("rules policies = %v, want ACCEPT/ACCEPT", rules)
	}
	for _, dir := range []string{"inbound", "outbound"} {
		list, _ := rules[dir].([]any)
		if len(list) != 3 {
			t.Errorf("%s rules = %d, want 3", dir, len(list))
		}
	}
	if fw.ID != 77 || fw.Region != "us-east" {
		t.Errorf("firewall = %+v", fw)
	}
}

// TestClient_EnsureAllowAllFirewallReuses checks an existing tagged firewall in
// the region is returned without creating a new one.
func TestClient_EnsureAllowAllFirewallReuses(t *testing.T) {
	var posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/networking/firewalls":
			fmt.Fprint(w, `{"data":[{"id":5,"label":"other","region":"us-west","tags":[]},{"id":9,"label":"linode-panel-allow-all","region":"us-east","tags":["linode-panel-allow-all"]}],"page":1,"pages":1}`)
		case r.Method == http.MethodPost && r.URL.Path == "/networking/firewalls":
			posts.Add(1)
			fmt.Fprint(w, `{"id":99,"region":"us-east"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"errors":[{"reason":"not found"}]}`)
		}
	}))
	defer srv.Close()

	c := New("t")
	c.BaseURL = srv.URL
	id, err := c.EnsureAllowAllFirewall(context.Background(), "us-east")
	if err != nil {
		t.Fatalf("EnsureAllowAllFirewall error: %v", err)
	}
	if id != 9 {
		t.Errorf("id = %d, want 9 (reused)", id)
	}
	if posts.Load() != 0 {
		t.Errorf("unexpected POST count = %d", posts.Load())
	}
}

// TestClient_EnsureAllowAllFirewallCreates checks a missing firewall is created.
func TestClient_EnsureAllowAllFirewallCreates(t *testing.T) {
	var posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/networking/firewalls":
			fmt.Fprint(w, `{"data":[],"page":1,"pages":1}`)
		case r.Method == http.MethodPost && r.URL.Path == "/networking/firewalls":
			posts.Add(1)
			fmt.Fprint(w, `{"id":42,"region":"us-east"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"errors":[{"reason":"not found"}]}`)
		}
	}))
	defer srv.Close()

	c := New("t")
	c.BaseURL = srv.URL
	id, err := c.EnsureAllowAllFirewall(context.Background(), "us-east")
	if err != nil {
		t.Fatalf("EnsureAllowAllFirewall error: %v", err)
	}
	if id != 42 || posts.Load() != 1 {
		t.Errorf("id = %d posts = %d, want 42 / 1", id, posts.Load())
	}
}

// TestClient_EnsureAllowAllFirewallReusesByLabel checks a firewall in the region
// whose label matches (but whose tag was stripped) is reused, not recreated.
func TestClient_EnsureAllowAllFirewallReusesByLabel(t *testing.T) {
	var posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/networking/firewalls":
			fmt.Fprint(w, `{"data":[{"id":11,"label":"linode-panel-allow-all-us-east","region":"us-east","tags":[]}],"page":1,"pages":1}`)
		case r.Method == http.MethodPost && r.URL.Path == "/networking/firewalls":
			posts.Add(1)
			fmt.Fprint(w, `{"id":99,"region":"us-east"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"errors":[{"reason":"not found"}]}`)
		}
	}))
	defer srv.Close()

	c := New("t")
	c.BaseURL = srv.URL
	id, err := c.EnsureAllowAllFirewall(context.Background(), "us-east")
	if err != nil {
		t.Fatalf("EnsureAllowAllFirewall error: %v", err)
	}
	if id != 11 || posts.Load() != 0 {
		t.Errorf("id = %d posts = %d, want 11 / 0", id, posts.Load())
	}
}

// TestClient_EnsureAllowAllFirewallAdoptsLegacyLabel checks a firewall created
// by an older panel version (region-less label, no tag) in the same region is
// adopted instead of colliding on create.
func TestClient_EnsureAllowAllFirewallAdoptsLegacyLabel(t *testing.T) {
	var posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/networking/firewalls":
			fmt.Fprint(w, `{"data":[{"id":12,"label":"linode-panel-allow-all","region":"us-east","tags":[]}],"page":1,"pages":1}`)
		case r.Method == http.MethodPost && r.URL.Path == "/networking/firewalls":
			posts.Add(1)
			fmt.Fprint(w, `{"id":99,"region":"us-east"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"errors":[{"reason":"not found"}]}`)
		}
	}))
	defer srv.Close()

	c := New("t")
	c.BaseURL = srv.URL
	id, err := c.EnsureAllowAllFirewall(context.Background(), "us-east")
	if err != nil {
		t.Fatalf("EnsureAllowAllFirewall error: %v", err)
	}
	if id != 12 || posts.Load() != 0 {
		t.Errorf("id = %d posts = %d, want 12 / 0", id, posts.Load())
	}
}

// TestClient_EnsureAllowAllFirewallCreatesForOtherRegion is the regression test
// for the reported 400: an allow-all firewall already exists in another region,
// so a request for a different region must create a region-scoped label rather
// than reusing the account-unique bare label.
func TestClient_EnsureAllowAllFirewallCreatesForOtherRegion(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/networking/firewalls":
			fmt.Fprint(w, `{"data":[{"id":9,"label":"linode-panel-allow-all-us-west","region":"us-west","tags":["linode-panel-allow-all"]}],"page":1,"pages":1}`)
		case r.Method == http.MethodPost && r.URL.Path == "/networking/firewalls":
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			fmt.Fprint(w, `{"id":42,"region":"ap-northeast"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"errors":[{"reason":"not found"}]}`)
		}
	}))
	defer srv.Close()

	c := New("t")
	c.BaseURL = srv.URL
	id, err := c.EnsureAllowAllFirewall(context.Background(), "ap-northeast")
	if err != nil {
		t.Fatalf("EnsureAllowAllFirewall error: %v", err)
	}
	if id != 42 {
		t.Errorf("id = %d, want 42 (created in ap-northeast)", id)
	}
	if gotBody["label"] != allowAllFirewallLabel("ap-northeast") {
		t.Errorf("create label = %v, want %q", gotBody["label"], allowAllFirewallLabel("ap-northeast"))
	}
}

// TestClient_EnsureAllowAllFirewallRaceReuses checks a duplicate-label 400 on
// create re-lists and reuses the firewall that won the race.
func TestClient_EnsureAllowAllFirewallRaceReuses(t *testing.T) {
	var lists atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/networking/firewalls":
			if lists.Add(1) == 1 {
				fmt.Fprint(w, `{"data":[],"page":1,"pages":1}`)
				return
			}
			fmt.Fprint(w, `{"data":[{"id":55,"label":"linode-panel-allow-all-ap-northeast","region":"ap-northeast","tags":["linode-panel-allow-all"]}],"page":1,"pages":1}`)
		case r.Method == http.MethodPost && r.URL.Path == "/networking/firewalls":
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"errors":[{"reason":"Label must be unique among your Cloud Firewalls."}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"errors":[{"reason":"not found"}]}`)
		}
	}))
	defer srv.Close()

	c := New("t")
	c.BaseURL = srv.URL
	id, err := c.EnsureAllowAllFirewall(context.Background(), "ap-northeast")
	if err != nil {
		t.Fatalf("EnsureAllowAllFirewall error: %v", err)
	}
	if id != 55 {
		t.Errorf("id = %d, want 55 (reused after duplicate-label 400)", id)
	}
}

// TestClient_AttachAllowAllFirewallAttaches drives ensure + device list + POST.
func TestClient_AttachAllowAllFirewallAttaches(t *testing.T) {
	var gotPath, gotMethod string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/networking/firewalls":
			fmt.Fprint(w, `{"data":[{"id":9,"label":"linode-panel-allow-all","region":"us-east","tags":["linode-panel-allow-all"]}],"page":1,"pages":1}`)
		case r.Method == http.MethodGet && r.URL.Path == "/networking/firewalls/9/devices":
			fmt.Fprint(w, `{"data":[],"page":1,"pages":1}`)
		case r.Method == http.MethodPost && r.URL.Path == "/networking/firewalls/9/devices":
			gotMethod, gotPath = r.Method, r.URL.Path
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			fmt.Fprint(w, `{"id":1,"entity":{"id":99,"type":"linode","label":"web"}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"errors":[{"reason":"not found"}]}`)
		}
	}))
	defer srv.Close()

	c := New("t")
	c.BaseURL = srv.URL
	if err := c.AttachAllowAllFirewall(context.Background(), "us-east", 99); err != nil {
		t.Fatalf("AttachAllowAllFirewall error: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/networking/firewalls/9/devices" {
		t.Errorf("attach request = %s %s", gotMethod, gotPath)
	}
	if gotBody["id"] != float64(99) || gotBody["type"] != "linode" {
		t.Errorf("attach body = %v", gotBody)
	}
}

// TestClient_AttachAllowAllFirewallNoop checks an already-attached Linode is a
// no-op (no POST).
func TestClient_AttachAllowAllFirewallNoop(t *testing.T) {
	var posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/networking/firewalls":
			fmt.Fprint(w, `{"data":[{"id":9,"label":"linode-panel-allow-all","region":"us-east","tags":["linode-panel-allow-all"]}],"page":1,"pages":1}`)
		case r.Method == http.MethodGet && r.URL.Path == "/networking/firewalls/9/devices":
			fmt.Fprint(w, `{"data":[{"id":1,"entity":{"id":99,"type":"linode","label":"web"}}],"page":1,"pages":1}`)
		case r.Method == http.MethodPost:
			posts.Add(1)
			fmt.Fprint(w, `{}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"errors":[{"reason":"not found"}]}`)
		}
	}))
	defer srv.Close()

	c := New("t")
	c.BaseURL = srv.URL
	if err := c.AttachAllowAllFirewall(context.Background(), "us-east", 99); err != nil {
		t.Fatalf("AttachAllowAllFirewall error: %v", err)
	}
	if posts.Load() != 0 {
		t.Errorf("unexpected POST count = %d, want no-op", posts.Load())
	}
}
