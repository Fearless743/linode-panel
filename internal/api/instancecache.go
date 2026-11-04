package api

import (
	"context"
	"log/slog"
	"time"

	"linode-panel/internal/linode"
)

// InstanceCacheMonitor 定期刷新所有账号的实例快照缓存。
//
// 列表接口默认读缓存（仅 ?refresh=1 才强制请求 Linode API），因此没有后台
// 刷新时缓存会一直陈旧直到用户手动刷新。该 monitor 每 tick 遍历所有用户
// 的全部 PAT，调用 ListInstances 并写入 CacheInstances，使列表页在不手动
// 刷新的情况下也能在 1 个 tick 内看到最新状态。单账号失败只记日志并保留
// 旧缓存，不影响其他账号。
type InstanceCacheMonitor struct {
	srv *Server
	tick time.Duration
	log  *slog.Logger

	// clientFor 按 PAT 构造 Linode 客户端，测试可注入 fake。
	clientFor func(pat string) LinodeAPI
}

// StartInstanceCacheMonitor 启动后台实例缓存刷新，ctx 取消即退出。
// tick <= 0 时退化为 time.Minute。启动后先刷新一次，别等一个完整 tick。
func (s *Server) StartInstanceCacheMonitor(ctx context.Context, tick time.Duration) *InstanceCacheMonitor {
	if tick <= 0 {
		tick = time.Minute
	}
	m := &InstanceCacheMonitor{
		srv:  s,
		tick: tick,
		log:  slog.Default(),
	}
	m.clientFor = func(pat string) LinodeAPI { return s.clientFor(pat) }
	s.cacheMonitor = m
	go m.run(ctx)
	return m
}

func (m *InstanceCacheMonitor) run(ctx context.Context) {
	ticker := time.NewTicker(m.tick)
	defer ticker.Stop()
	m.tickOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.tickOnce(ctx)
		}
	}
}

// tickOnce 刷新一次全量缓存：每个用户的所有 PAT 各请求一次 Linode API。
func (m *InstanceCacheMonitor) tickOnce(ctx context.Context) {
	for _, u := range m.srv.Store.Users() {
		for _, tok := range u.Tokens {
			if tok.ID <= 0 || tok.Token == "" {
				continue
			}
			if ctx.Err() != nil {
				return
			}
			m.refreshToken(ctx, tok.ID, tok.Label, tok.Token)
		}
	}
}

func (m *InstanceCacheMonitor) refreshToken(ctx context.Context, tokenID int, label, pat string) {
	tctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	insts, err := m.clientFor(pat).ListInstances(tctx)
	log := m.log
	if log == nil {
		log = slog.Default()
	}
	if err != nil {
		log.Warn("实例缓存刷新失败，保留旧缓存", "token_id", tokenID, "label", label, "error", err)
		return
	}
	if insts == nil {
		insts = []linode.Instance{}
	}
	if err := m.srv.Store.CacheInstances(tokenID, insts); err != nil {
		log.Warn("实例缓存写入失败", "token_id", tokenID, "label", label, "error", err)
	}
}
