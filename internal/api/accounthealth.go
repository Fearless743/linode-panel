package api

import (
	"context"
	"fmt"

	"linode-panel/internal/linode"
)

// ---------- 账号健康探测 ----------

// accountHealthProber 抽象「按 PAT 获取账号信息」，便于测试注入。
// *linode.Client 天然满足该接口（实现 GetAccount）。
type accountHealthProber interface {
	GetAccount(ctx context.Context) (linode.Account, error)
}

// probeAccountHealth 探测一个账号（PAT）的健康状况。
// 返回 status ∈ HealthOK|HealthNoBalance|HealthBanned|HealthError，以及可读的中文 detail。
//
// 默认用服务端按 PAT 缓存的 Linode 客户端发起 GET /account，把结果交给
// probeAccountHealthWith 做判定。纯探测：不落库、不起 goroutine。
func (s *Server) probeAccountHealth(ctx context.Context, pat string) (status string, detail string) {
	return s.probeAccountHealthWith(ctx, s.clientFor(pat))
}

// probeAccountHealthWith 用注入的 prober 做判定，probeAccountHealth 是其默认实现（用 s.clientFor(pat)）。
//
// 判定顺序：
//  1. GetAccount 出错：ClassifyAccountError 能归类则返回该状态（封号/余额不足），
//     否则视为「检测本身失败」返回 HealthError。
//  2. 成功：余额（欠款或未出账）超过阈值即 HealthNoBalance。
//  3. 否则 HealthOK。
func (s *Server) probeAccountHealthWith(ctx context.Context, p accountHealthProber) (status, detail string) {
	acct, err := p.GetAccount(ctx)
	if err != nil {
		switch ClassifyAccountError(err) {
		case HealthBanned:
			return HealthBanned, "账号不可用: " + err.Error()
		case HealthNoBalance:
			return HealthNoBalance, "账号余额不足: " + err.Error()
		}
		return HealthError, "查询账号失败: " + err.Error()
	}
	if BalanceExhausted(acct.Balance, acct.BalanceUninvoiced, DefaultBalanceThreshold) {
		return HealthNoBalance, fmt.Sprintf("余额不足：欠款 %.2f，未出账 %.2f", acct.Balance, acct.BalanceUninvoiced)
	}
	return HealthOK, fmt.Sprintf("余额正常：欠款 %.2f，未出账 %.2f", acct.Balance, acct.BalanceUninvoiced)
}
