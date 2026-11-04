package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"linode-panel/internal/store"
)

// newDualServer seeds admin + one PAT and returns server+session.
func newDualServer(t *testing.T) (*Server, string) {
	t.Helper()
	st, _, err := store.Open(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	if _, err := st.AddToken(1, "acc", "pat-acc"); err != nil {
		t.Fatalf("AddToken: %v", err)
	}
	session := st.NewSession(1, time.Hour)
	s := New(&fakeAPI{}, st)
	t.Cleanup(func() { _ = st.Save() })
	return s, session
}

func TestDualStoreRoundTrip(t *testing.T) {
	st, _, err := store.Open(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	cfg := store.DualConfig{
		TokenID: 1, UserID: 1, Enabled: true, Port: 22,
		Threshold: 3, IntervalSec: 300, ActiveInstanceID: 101,
		Script: "#!/bin/bash\necho hi",
	}
	if err := st.SetDualConfig(cfg, "Secret123!"); err != nil {
		t.Fatalf("SetDualConfig: %v", err)
	}
	got, ok := st.DualConfigOf(1)
	if !ok || !got.Enabled || got.ActiveInstanceID != 101 {
		t.Fatalf("DualConfigOf = %+v,%v", got, ok)
	}
	if got.Script != "#!/bin/bash\necho hi" || !got.HasRootPass {
		t.Fatalf("script/pass not saved: %+v", got)
	}
	pw, ok := st.DualRootPassword(1)
	if !ok || pw != "Secret123!" {
		t.Fatalf("DualRootPassword = %q,%v", pw, ok)
	}
	if err := st.UpdateDualInstances(1, 102, 101); err != nil {
		t.Fatalf("UpdateDualInstances: %v", err)
	}
	got, _ = st.DualConfigOf(1)
	if got.ActiveInstanceID != 102 || got.StandbyInstanceID != 101 {
		t.Fatalf("swap not saved: %+v", got)
	}
	if err := st.UpdateDualState(1, 2, "rotating", "blocked", "detail", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatalf("UpdateDualState: %v", err)
	}
	got, _ = st.DualConfigOf(1)
	if got.FailCount != 2 || got.State != "rotating" {
		t.Fatalf("state not saved: %+v", got)
	}
}

func TestDualPutRequiresTemplateAndPassword(t *testing.T) {
	srv, session := newDualServer(t)
	// 缺模板。
	body := `{"enabled":true,"port":22,"threshold":3,"interval_seconds":300}`
	req := httptest.NewRequest(http.MethodPut, "/api/account/dual/1", strings.NewReader(body))
	authed(req, session)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400 without template, got %d %s", rr.Code, rr.Body.String())
	}
	// 有模板但缓存/API 都没有该实例。
	body = `{"enabled":true,"template_instance_id":999,"port":22,"threshold":3,"interval_seconds":300,"root_password":"Secret123!"}`
	req = httptest.NewRequest(http.MethodPut, "/api/account/dual/1", strings.NewReader(body))
	authed(req, session)
	rr = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for missing template, got %d %s", rr.Code, rr.Body.String())
	}
}

func TestIsDualManagedSkipsAutoRebuild(t *testing.T) {
	srv, _ := newDualServer(t)
	if err := srv.Store.SetDualConfig(store.DualConfig{
		TokenID: 1, UserID: 1, Enabled: true, Port: 22,
		Threshold: 3, IntervalSec: 300, ActiveInstanceID: 42, StandbyInstanceID: 43,
	}, "Secret123!"); err != nil {
		t.Fatalf("SetDualConfig: %v", err)
	}
	if !srv.isDualManaged(42) || !srv.isDualManaged(43) {
		t.Fatal("dual instances should be managed")
	}
	if srv.isDualManaged(44) {
		t.Fatal("other instance should not be managed")
	}
	if got := srv.dualRoleFor(42); got != "active" {
		t.Fatalf("dualRoleFor(42) = %q, want active", got)
	}
	if got := srv.dualRoleFor(43); got != "standby" {
		t.Fatalf("dualRoleFor(43) = %q, want standby", got)
	}
}
