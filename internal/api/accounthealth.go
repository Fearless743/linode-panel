package api

import (
	"context"
	"fmt"
	"time"

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

// AccountHealth 是一次账号探测的完整结果：状态 + 中文说明 + 余额数字。
// 状态取值同 Health* 常量；探测失败（无法判断账号状态）时 status = HealthError。
type AccountHealth struct {
	Status    string  // HealthOK|HealthNoBalance|HealthBanned|HealthError
	Detail    string  // 可读中文说明
	Balance   float64 // 当前欠费（正数=欠款）；探测失败时 0
	Unbilled  float64 // 未出账金额；探测失败时 0
	CheckedAt string  // RFC3339 时间戳
}

// probeAccountHealthFull 探测账号完整信息（状态、详情、余额数字）。
// 比 probeAccountHealth 多返回余额，供 UI 直接展示。
// 一次 GetAccount 调用同时得出状态判定与余额数字，不重复请求。
func (s *Server) probeAccountHealthFull(ctx context.Context, pat string) AccountHealth {
	h := AccountHealth{CheckedAt: time.Now().UTC().Format(time.RFC3339)}
	acct, err := s.clientFor(pat).GetAccount(ctx)
	if err != nil {
		switch ClassifyAccountError(err) {
		case HealthBanned:
			h.Status = HealthBanned
			h.Detail = "账号不可用: " + err.Error()
		case HealthNoBalance:
			h.Status = HealthNoBalance
			h.Detail = "账号余额不足: " + err.Error()
		default:
			h.Status = HealthError
			h.Detail = "查询账号失败: " + err.Error()
		}
		return h
	}
	h.Balance = acct.Balance
	h.Unbilled = acct.BalanceUninvoiced
	if BalanceExhausted(acct.Balance, acct.BalanceUninvoiced, DefaultBalanceThreshold) {
		h.Status = HealthNoBalance
		h.Detail = fmt.Sprintf("余额不足：欠款 %.2f，未出账 %.2f", acct.Balance, acct.BalanceUninvoiced)
	} else {
		h.Status = HealthOK
		h.Detail = fmt.Sprintf("余额正常：欠款 %.2f，未出账 %.2f", acct.Balance, acct.BalanceUninvoiced)
	}
	return h
}

// probeAccountHealthWith 用注入的 prober 做判定，probeAccountHealth 是其默认实现（用 s.clientFor(pat)）。
//
// 判定顺序：
//  1. GetAccount 出错：ClassifyAccountError 能归类则返回该状态（封号/余额不足），
//     否则视为「检测本身失败」返回 HealthError。
//  2. 成功：余额（欠款或未出账）超过阈值即 HealthNoBalance。
//  3. 否则 HealthOK。
func (s *Server) probeAccountHealthWith(ctx context.Context, p accountHealthProber) (status, detail string) {
	return probeAccountHealthWith(p, ctx)
}

// probeAccountHealthWith 是注入版：便于测试替换 prober。
func probeAccountHealthWith(p accountHealthProber, ctx context.Context) (status, detail string) {
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
