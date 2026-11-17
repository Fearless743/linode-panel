package api

import (
	"net/http"
	"strconv"
	"testing"

	"linode-panel/internal/store"
)

// TestSelfHealStoreModes 验证统一配置在两种模式间切换时互斥且参数正确保存。
func TestSelfHealStoreModes(t *testing.T) {
	st, err := openTestStore(t)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUser("alice", "alice123", 1); err != nil {
		t.Fatal(err)
	}
	tok, err := st.AddToken(aliceID, "acc", "pat")
	if err != nil {
		t.Fatal(err)
	}

	cfg := store.SelfHeal{
		TokenID: tok.ID, UserID: aliceID, Mode: store.SelfHealRebuild, Enabled: true,
		Port: 443, Threshold: 5, IntervalSec: 600, Script: "echo hi",
	}
	if err := st.SetSelfHeal(cfg, "secret"); err != nil {
		t.Fatalf("SetSelfHeal(rebuild): %v", err)
	}
	got := st.SelfHealOf(tok.ID)
	if got.Mode != store.SelfHealRebuild || !got.Enabled || got.Port != 443 ||
		got.Threshold != 5 || got.IntervalSec != 600 || got.Script != "echo hi" || !got.HasRootPass {
		t.Fatalf("rebuild view = %+v", got)
	}
	if all := st.SelfHeals(); len(all) != 1 || all[0].Mode != store.SelfHealRebuild {
		t.Fatalf("SelfHeals = %+v", all)
	}

	// 切到 dual：自动换 IP 开关应被清掉，A/B 指针生效。
	cfg.Mode, cfg.Enabled, cfg.ActiveInstanceID, cfg.StandbyInstanceID = store.SelfHealDual, true, 11, 12
	if err := st.SetSelfHeal(cfg, ""); err != nil {
		t.Fatalf("SetSelfHeal(dual): %v", err)
	}
	got = st.SelfHealOf(tok.ID)
	ac, _ := st.AccountConfig(tok.ID)
	if got.Mode != store.SelfHealDual || got.ActiveInstanceID != 11 || got.StandbyInstanceID != 12 {
		t.Fatalf("dual view = %+v", got)
	}
	if ac.Enabled {
		t.Fatal("切到 dual 后 auto_enabled 应为 false")
	}

	if err := st.DeleteSelfHeal(tok.ID); err != nil {
		t.Fatal(err)
	}
	if got := st.SelfHealOf(tok.ID); got.Mode != store.SelfHealOff || got.Enabled {
		t.Fatalf("after delete = %+v", got)
	}
	if all := st.SelfHeals(); len(all) != 0 {
		t.Fatalf("after delete SelfHeals = %+v", all)
	}
}

// TestSelfHealAPI 验证统一配置接口可保存/读取/关闭，并只列出当前用户的配置。
func TestSelfHealAPI(t *testing.T) {
	srv := testServer(t, &fakeAPI{})
	addAliceToken(t, srv, "acc", "pat")
	cookie := loginAlice(t, srv)
	tok := srv.Store.TokensOf(aliceID)[0]
	path := "/api/account/self-heal/" + strconv.Itoa(tok.ID)

	body := `{"mode":"rebuild","enabled":true,"port":443,"threshold":4,"interval_seconds":600,"script":"echo hi","root_password":"secret"}`
	rr := authedPut(t, srv, cookie, path, body)
	wantStatus(t, rr, http.StatusOK)
	var cfg store.SelfHeal
	decodeJSON(t, rr, &cfg)
	if cfg.Mode != store.SelfHealRebuild || cfg.Port != 443 || cfg.Threshold != 4 || !cfg.HasRootPass {
		t.Fatalf("put response = %+v", cfg)
	}

	rr = authedGet(t, srv, cookie, path)
	wantStatus(t, rr, http.StatusOK)
	decodeJSON(t, rr, &cfg)
	if cfg.Mode != store.SelfHealRebuild || cfg.Script != "echo hi" {
		t.Fatalf("get response = %+v", cfg)
	}

	rr = authedGet(t, srv, cookie, "/api/account/self-heal")
	wantStatus(t, rr, http.StatusOK)
	var list struct {
		SelfHeal []store.SelfHeal `json:"self_heal"`
	}
	decodeJSON(t, rr, &list)
	if len(list.SelfHeal) != 1 || list.SelfHeal[0].TokenID != tok.ID {
		t.Fatalf("list = %+v", list)
	}

	rr = authedDelete(t, srv, cookie, path)
	wantStatus(t, rr, http.StatusOK)
	cfg = srv.Store.SelfHealOf(tok.ID)
	if cfg.Mode != store.SelfHealOff {
		t.Fatalf("after delete = %+v", cfg)
	}
}
