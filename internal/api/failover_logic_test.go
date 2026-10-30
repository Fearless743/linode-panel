package api

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"linode-panel/internal/linode"
)

// TestFailoverClassifyAccountError 覆盖账号级错误归类的各种情形。
func TestFailoverClassifyAccountError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"普通错误", errors.New("boom"), ""},
		{"403 含 suspended", &linode.APIError{StatusCode: http.StatusForbidden, Reason: "Account suspended"}, HealthBanned},
		{"403 不含关键词", &linode.APIError{StatusCode: http.StatusForbidden, Reason: "Forbidden"}, HealthBanned},
		{"401 含 deactivated", &linode.APIError{StatusCode: http.StatusUnauthorized, Reason: "account deactivated"}, HealthBanned},
		{"401 含 not authorized", &linode.APIError{StatusCode: http.StatusUnauthorized, Reason: "Not Authorized"}, HealthBanned},
		{"403 含 banned", &linode.APIError{StatusCode: http.StatusForbidden, Reason: "You have been BANNED"}, HealthBanned},
		{"400 含 insufficient", &linode.APIError{StatusCode: http.StatusBadRequest, Reason: "Insufficient funds"}, HealthNoBalance},
		{"400 含 not enough", &linode.APIError{StatusCode: http.StatusBadRequest, Reason: "not enough balance"}, HealthNoBalance},
		{"400 含 balance", &linode.APIError{StatusCode: http.StatusBadRequest, Reason: "Balance is too low"}, HealthNoBalance},
		{"400 不含关键词", &linode.APIError{StatusCode: http.StatusBadRequest, Reason: "Bad Request"}, ""},
		{"500", &linode.APIError{StatusCode: http.StatusInternalServerError, Reason: "internal"}, ""},
		{"404", &linode.APIError{StatusCode: http.StatusNotFound, Reason: "not found"}, ""},
		{
			"包装后的 403",
			fmt.Errorf("调用账号接口: %w", &linode.APIError{StatusCode: http.StatusForbidden, Reason: "suspended"}),
			HealthBanned,
		},
		{
			"包装后的 400",
			fmt.Errorf("创建实例: %w", &linode.APIError{StatusCode: http.StatusBadRequest, Reason: "Insufficient funds"}),
			HealthNoBalance,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyAccountError(tc.err); got != tc.want {
				t.Fatalf("ClassifyAccountError(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

// TestFailoverBalanceExhausted 覆盖余额不足判定的边界。
func TestFailoverBalanceExhausted(t *testing.T) {
	cases := []struct {
		name       string
		balance    float64
		uninvoiced float64
		threshold  float64
		want       bool
	}{
		{"零余额零欠款", 0, 0, DefaultBalanceThreshold, false},
		{"负余额（有结余）", -10, 0, DefaultBalanceThreshold, false},
		{"正欠款超阈值", 5, 0, DefaultBalanceThreshold, true},
		{"未出账超阈值", 0, 3.5, DefaultBalanceThreshold, true},
		{"刚好等于阈值", 0, 0, 0, false},
		{"余额等于正阈值", 2, 0, 2, false},
		{"余额略超正阈值", 2.01, 0, 2, true},
		{"未出账等于阈值", 0, 1, 1, false},
		{"高阈值不触发", 1, 1, 10, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := BalanceExhausted(tc.balance, tc.uninvoiced, tc.threshold); got != tc.want {
				t.Fatalf("BalanceExhausted(%v, %v, %v) = %v, want %v",
					tc.balance, tc.uninvoiced, tc.threshold, got, tc.want)
			}
		})
	}
}

// TestFailoverNextReplacement 覆盖替换目标的轮选逻辑。
func TestFailoverNextReplacement(t *testing.T) {
	cases := []struct {
		name    string
		targets []int
		start   int
		exclude int
		wantID  int
		wantIdx int
	}{
		{"正常轮选", []int{11, 22, 33}, 0, 0, 11, 0},
		{"从中间开始", []int{11, 22, 33}, 1, 0, 22, 1},
		{"从末尾开始", []int{11, 22, 33}, 2, 0, 33, 2},
		{"start 越界规整为 0", []int{11, 22, 33}, 9, 0, 11, 0},
		{"start 负数规整为 0", []int{11, 22, 33}, -3, 0, 11, 0},
		{"排除首个", []int{11, 22, 33}, 0, 11, 22, 1},
		{"排除后环形回绕", []int{11, 22, 33}, 2, 33, 11, 0},
		{"全部被排除", []int{11, 11, 11}, 0, 11, -1, -1},
		{"exclude<=0 不排除", []int{11, 22}, 0, -1, 11, 0},
		{"空候选", []int{}, 0, 0, -1, -1},
		{"仅一个候选", []int{42}, 0, 0, 42, 0},
		{"仅一个候选被排除", []int{42}, 0, 42, -1, -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// 快照入参，确认纯函数不改动切片。
			before := append([]int(nil), tc.targets...)
			gotID, gotIdx := NextReplacement(tc.targets, tc.start, tc.exclude)
			if gotID != tc.wantID || gotIdx != tc.wantIdx {
				t.Fatalf("NextReplacement(%v, %d, %d) = (%d, %d), want (%d, %d)",
					tc.targets, tc.start, tc.exclude, gotID, gotIdx, tc.wantID, tc.wantIdx)
			}
			if len(before) != len(tc.targets) {
				t.Fatalf("入参切片被修改: before=%v after=%v", before, tc.targets)
			}
			for i := range before {
				if before[i] != tc.targets[i] {
					t.Fatalf("入参切片被修改: before=%v after=%v", before, tc.targets)
				}
			}
		})
	}
}
