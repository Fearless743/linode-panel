package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"linode-panel/internal/linode"
)

// fakeAccountProber 是 accountHealthProber 的测试替身：直接返回预置的账号或错误。
type fakeAccountProber struct {
	acct linode.Account
	err  error
}

func (f fakeAccountProber) GetAccount(ctx context.Context) (linode.Account, error) {
	return f.acct, f.err
}

// TestAccountHealthProbeWith 覆盖 probeAccountHealthWith 的各种判定分支。
func TestAccountHealthProbeWith(t *testing.T) {
	srv := testServer(t, &fakeAPI{})
	cases := []struct {
		name       string
		acct       linode.Account
		err        error
		wantStatus string
		wantDetail string // 期望 detail 包含的子串
	}{
		{
			name:       "余额正常（有零余额）",
			acct:       linode.Account{Balance: 0, BalanceUninvoiced: 0},
			wantStatus: HealthOK,
			wantDetail: "余额正常：欠款 0.00，未出账 0.00",
		},
		{
			name:       "余额正常（有结余）",
			acct:       linode.Account{Balance: -3.5, BalanceUninvoiced: 0},
			wantStatus: HealthOK,
			wantDetail: "余额正常：欠款 -3.50，未出账 0.00",
		},
		{
			name:       "余额不足（欠款超阈值）",
			acct:       linode.Account{Balance: 5.5, BalanceUninvoiced: 0},
			wantStatus: HealthNoBalance,
			wantDetail: "余额不足：欠款 5.50，未出账 0.00",
		},
		{
			name:       "余额不足（未出账超阈值）",
			acct:       linode.Account{Balance: 0, BalanceUninvoiced: 2.75},
			wantStatus: HealthNoBalance,
			wantDetail: "余额不足：欠款 0.00，未出账 2.75",
		},
		{
			name:       "阈值边界：余额恰好等于阈值不算不足",
			acct:       linode.Account{Balance: DefaultBalanceThreshold, BalanceUninvoiced: DefaultBalanceThreshold},
			wantStatus: HealthOK,
			wantDetail: "余额正常",
		},
		{
			name:       "封号（403）",
			err:        &linode.APIError{StatusCode: http.StatusForbidden, Reason: "Account suspended"},
			wantStatus: HealthBanned,
			wantDetail: "账号不可用",
		},
		{
			name:       "资金不足（400 insufficient）",
			err:        &linode.APIError{StatusCode: http.StatusBadRequest, Reason: "Insufficient funds"},
			wantStatus: HealthNoBalance,
			wantDetail: "账号余额不足",
		},
		{
			name:       "其它错误（500 → 检测失败）",
			err:        &linode.APIError{StatusCode: http.StatusInternalServerError, Reason: "internal"},
			wantStatus: HealthError,
			wantDetail: "查询账号失败",
		},
		{
			name:       "普通网络错误（→ 检测失败）",
			err:        errors.New("connection refused"),
			wantStatus: HealthError,
			wantDetail: "查询账号失败: connection refused",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, detail := srv.probeAccountHealthWith(context.Background(),
				fakeAccountProber{acct: tc.acct, err: tc.err})
			if status != tc.wantStatus {
				t.Fatalf("status = %q, want %q (detail=%q)", status, tc.wantStatus, detail)
			}
			if !strings.Contains(detail, tc.wantDetail) {
				t.Fatalf("detail = %q, want contains %q", detail, tc.wantDetail)
			}
		})
	}
}

// TestProbeAccountHealthHTTP 验证 probeAccountHealth 走 s.clientFor(pat) 的真实接线：
// 用 httptest 冒充 Linode API 的 GET /account。
func TestProbeAccountHealthHTTP(t *testing.T) {
	var gotAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/account" {
			http.NotFound(w, r)
			return
		}
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"balance":12.5,"balance_uninvoiced":0.5}`))
	}))
	defer ts.Close()

	srv := testServer(t, &fakeAPI{})
	srv.APIBase = ts.URL

	status, detail := srv.probeAccountHealth(context.Background(), "linode_pat_probe")
	if gotAuth != "Bearer linode_pat_probe" {
		t.Fatalf("Authorization = %q, want %q", gotAuth, "Bearer linode_pat_probe")
	}
	if status != HealthNoBalance {
		t.Fatalf("status = %q, want %q (detail=%q)", status, HealthNoBalance, detail)
	}
	if !strings.Contains(detail, "余额不足：欠款 12.50，未出账 0.50") {
		t.Fatalf("detail = %q", detail)
	}
}

// TestProbeAccountHealthHTTPBanned 验证错误路径也经由真实 HTTP 客户端归类为封号。
func TestProbeAccountHealthHTTPBanned(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"errors":[{"reason":"Account suspended"}]}`))
	}))
	defer ts.Close()

	srv := testServer(t, &fakeAPI{})
	srv.APIBase = ts.URL

	status, _ := srv.probeAccountHealth(context.Background(), "linode_pat_banned")
	if status != HealthBanned {
		t.Fatalf("status = %q, want %q", status, HealthBanned)
	}
}
