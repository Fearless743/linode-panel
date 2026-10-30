package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"linode-panel/internal/linode"
)

// DefaultFailover* are the per-group defaults applied when the UI leaves a
// field empty.
const (
	DefaultFailoverThreshold = 3
	DefaultFailoverInterval  = 5 * time.Minute
	// DefaultBalanceThreshold 为「余额不足」判定的欠款阈值（正数=欠款）。
	DefaultBalanceThreshold = 0.0
)

// 账号健康三态（用于本任务及后续监控）。
const (
	HealthOK        = "ok"
	HealthBlocked   = "blocked"    // 被墙（沿用 autorebuild 语义）
	HealthNoBalance = "no_balance" // 没有余额
	HealthBanned    = "banned"     // 封号/账号不可用
	HealthError     = "error"      // 检测本身失败
)

// ---------- 账号错误归类 ----------

// ClassifyAccountError 把一次账号级 API 调用的错误归类为
// HealthNoBalance / HealthBanned / ""（未识别）。
//
// 判定规则：
//   - 错误链里能取到 *linode.APIError：
//   - StatusCode 401/403：若消息含 suspended / deactivated / banned /
//     disabled / "not authorized"（不区分大小写）→ HealthBanned；
//     否则 401/403 也统一视为 HealthBanned（凭证失效/账号不可用）。
//   - StatusCode 400：消息含 insufficient / "not enough" / balance /
//     funds（不区分大小写）→ HealthNoBalance。
//   - 否则返回 ""。
func ClassifyAccountError(err error) string {
	if err == nil {
		return ""
	}
	var apiErr *linode.APIError
	if !errors.As(err, &apiErr) {
		return ""
	}
	msg := strings.ToLower(apiErr.Reason)
	switch apiErr.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		// 401/403 一律视为账号不可用（凭证失效或封号），关键词仅用于区分
		// 「封号」与其它不可用原因，这里两者归宿相同，无需分支。
		return HealthBanned
	case http.StatusBadRequest:
		if containsAny(msg, "insufficient", "not enough", "balance", "funds") {
			return HealthNoBalance
		}
	}
	return ""
}

// containsAny 报告 s 是否含 keywords 中任一子串（s 已由调用方转小写）。
func containsAny(s string, keywords ...string) bool {
	for _, kw := range keywords {
		if strings.Contains(s, kw) {
			return true
		}
	}
	return false
}

// BalanceExhausted 判断余额是否不足以继续开机：欠款(balance, 正数表示欠费)
// 或未出账(uninvoiced)任一超过阈值 threshold 即视为不足。
func BalanceExhausted(balance, uninvoiced, threshold float64) bool {
	return balance > threshold || uninvoiced > threshold
}

// NextReplacement 在候选账号列表中轮选下一个替换目标：
//   - targets 为组内候选 Token id（调用方保证不含故障账号自身），已按其自身顺序给出；
//   - start 为起始下标（>=0，越界会被规整回 0）；
//   - exclude 为要排除的 Token id（<=0 表示不排除）。
//
// 返回选中的 Token id 与其下标；无可用目标时返回 (-1, -1)。
func NextReplacement(targets []int, start, exclude int) (int, int) {
	n := len(targets)
	if n == 0 {
		return -1, -1
	}
	if start < 0 || start >= n {
		start = 0
	}
	// 从 start 起环形轮询，跳过被排除的 Token id，最多绕一整圈。
	for i := 0; i < n; i++ {
		idx := (start + i) % n
		if exclude > 0 && targets[idx] == exclude {
			continue
		}
		return targets[idx], idx
	}
	return -1, -1
}
