package api

import (
	"fmt"
	"net/http"
	"testing"

	"linode-panel/internal/linode"
)

// TestMigrateInstanceHTTP verifies a missing (upstream 404) instance can be
// recreated on another account from its cached spec, with the stable cache row
// and panel-side configuration moving to the new account/instance.
func TestMigrateInstanceHTTP(t *testing.T) {
	mock := mockLinodeAPI(t)
	defer mock.Close()
	srv := testServer(t, &fakeAPI{})
	srv.APIBase = mock.URL
	addAliceToken(t, srv, "old", "pat_old")
	addAliceToken(t, srv, "new", "pat_new")
	cookie := loginAlice(t, srv)

	toks := srv.Store.TokensOf(aliceID)
	if len(toks) != 2 {
		t.Fatalf("tokens = %+v, want 2", toks)
	}
	src, dst := toks[0].ID, toks[1].ID
	if err := srv.Store.CacheInstances(src, []linode.Instance{{
		ID: 42, Label: "web", Status: "running", Region: "us-east",
		Type: "g6-standard-1", Image: "linode/debian12",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := srv.Store.SetInstanceRootPassword(42, "Secret123!"); err != nil {
		t.Fatal(err)
	}
	if err := srv.Store.MarkInstanceMissing(src, 42); err != nil {
		t.Fatal(err)
	}

	body := fmt.Sprintf(`{"token_id":%d,"target_token_id":%d}`, src, dst)
	rr := authedPost(t, srv, cookie, "/api/instances/42/migrate", body)
	wantStatus(t, rr, http.StatusOK)
	var resp struct {
		Instance struct {
			ID      int    `json:"id"`
			Label   string `json:"label"`
			TokenID int    `json:"token_id"`
		} `json:"instance"`
		OldInstanceID int    `json:"old_instance_id"`
		TokenID       int    `json:"token_id"`
		TokenLabel    string `json:"token_label"`
	}
	decodeJSON(t, rr, &resp)
	if resp.Instance.ID != 100 || resp.OldInstanceID != 42 ||
		resp.TokenID != dst || resp.Instance.TokenID != dst || resp.TokenLabel != "new" {
		t.Fatalf("migrate response = %+v", resp)
	}

	// The row moved to the target account and points at the new instance.
	if rows := srv.Store.CachedInstanceRows(src); len(rows) != 0 {
		t.Fatalf("source rows = %+v, want none", rows)
	}
	rows := srv.Store.CachedInstanceRows(dst)
	if len(rows) != 1 || rows[0].Instance.ID != 100 || !rows[0].Alive {
		t.Fatalf("target rows = %+v, want instance 100 alive", rows)
	}
	// The saved root password follows the stable cache id.
	if pass, ok := srv.Store.InstanceRootPassword(100); !ok || pass != "Secret123!" {
		t.Fatalf("InstanceRootPassword(100) = %q, %v", pass, ok)
	}
}

// TestMigrateInstanceRequiresRootPassword verifies migration refuses to create
// an instance when neither the request nor the cache has a root password.
func TestMigrateInstanceRequiresRootPassword(t *testing.T) {
	mock := mockLinodeAPI(t)
	defer mock.Close()
	srv := testServer(t, &fakeAPI{})
	srv.APIBase = mock.URL
	addAliceToken(t, srv, "old", "pat_old")
	addAliceToken(t, srv, "new", "pat_new")
	cookie := loginAlice(t, srv)

	toks := srv.Store.TokensOf(aliceID)
	src, dst := toks[0].ID, toks[1].ID
	if err := srv.Store.CacheInstances(src, []linode.Instance{{
		ID: 42, Label: "web", Region: "us-east",
		Type: "g6-standard-1", Image: "linode/debian12",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := srv.Store.MarkInstanceMissing(src, 42); err != nil {
		t.Fatal(err)
	}

	body := fmt.Sprintf(`{"token_id":%d,"target_token_id":%d}`, src, dst)
	rr := authedPost(t, srv, cookie, "/api/instances/42/migrate", body)
	wantStatus(t, rr, http.StatusBadRequest)
	// Nothing was moved.
	if rows := srv.Store.CachedInstanceRows(src); len(rows) != 1 || rows[0].Alive {
		t.Fatalf("source rows = %+v, want the missing row untouched", rows)
	}
}

// TestMigrateInstanceRejectsLiveInstance verifies migration is only for
// instances that are gone upstream.
func TestMigrateInstanceRejectsLiveInstance(t *testing.T) {
	srv := testServer(t, &fakeAPI{})
	addAliceToken(t, srv, "old", "pat_old")
	addAliceToken(t, srv, "new", "pat_new")
	cookie := loginAlice(t, srv)

	toks := srv.Store.TokensOf(aliceID)
	src, dst := toks[0].ID, toks[1].ID
	if err := srv.Store.CacheInstances(src, []linode.Instance{{
		ID: 42, Label: "web", Region: "us-east",
		Type: "g6-standard-1", Image: "linode/debian12",
	}}); err != nil {
		t.Fatal(err)
	}

	body := fmt.Sprintf(`{"token_id":%d,"target_token_id":%d}`, src, dst)
	rr := authedPost(t, srv, cookie, "/api/instances/42/migrate", body)
	wantStatus(t, rr, http.StatusConflict)
}
