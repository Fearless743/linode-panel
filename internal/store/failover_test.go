package store

import (
	"testing"
)

// TestFailoverCRUD 覆盖按实例替换配置的 upsert/读取/列表/删除。
func TestFailoverCRUD(t *testing.T) {
	s, _ := newTestStore(t)

	if _, ok := s.Failover(42); ok {
		t.Fatal("未配置的实例不应被查到")
	}

	cfg := Failover{
		InstanceID:  42,
		UserID:      1,
		GroupID:     3,
		TokenID:     7,
		Enabled:     true,
		Threshold:   3,
		IntervalSec: 300,
	}
	if err := s.SetFailover(cfg); err != nil {
		t.Fatalf("SetFailover: %v", err)
	}
	got, ok := s.Failover(42)
	if !ok {
		t.Fatal("SetFailover 后 Failover 未找到")
	}
	if !got.Enabled || got.GroupID != 3 || got.TokenID != 7 || got.Threshold != 3 || got.IntervalSec != 300 || got.UserID != 1 {
		t.Fatalf("got = %+v", got)
	}
	if got.UpdatedAt == "" {
		t.Fatal("UpdatedAt 未设置")
	}

	// 编辑设置项应保留监控状态。
	if err := s.UpdateFailoverState(42, 2, "blocked", "账号被封", "2026-01-01T00:00:00Z", "task-1"); err != nil {
		t.Fatalf("UpdateFailoverState: %v", err)
	}
	cfg.Threshold = 5
	cfg.TokenID = 9
	if err := s.SetFailover(cfg); err != nil {
		t.Fatalf("SetFailover update: %v", err)
	}
	got, _ = s.Failover(42)
	if got.Threshold != 5 || got.TokenID != 9 {
		t.Fatalf("设置项未更新: %+v", got)
	}
	if got.FailCount != 2 || got.LastStatus != "blocked" || got.LastTaskID != "task-1" {
		t.Fatalf("监控状态被覆盖: %+v", got)
	}

	list := s.Failovers()
	if len(list) != 1 || list[0].InstanceID != 42 {
		t.Fatalf("Failovers = %+v", list)
	}

	if err := s.DeleteFailover(42); err != nil {
		t.Fatalf("DeleteFailover: %v", err)
	}
	if _, ok := s.Failover(42); ok {
		t.Fatal("删除后配置仍存在")
	}
}

// TestFailoverUpdateState 覆盖状态更新语义：空字符串保持原值。
func TestFailoverUpdateState(t *testing.T) {
	s, _ := newTestStore(t)
	cfg := Failover{
		InstanceID: 42, UserID: 1, GroupID: 3, TokenID: 7, Enabled: true,
		Threshold: 3, IntervalSec: 300,
	}
	if err := s.SetFailover(cfg); err != nil {
		t.Fatalf("SetFailover: %v", err)
	}

	if err := s.UpdateFailoverState(42, 2, "no_balance", "余额不足", "2026-01-01T00:00:00Z", "task-1"); err != nil {
		t.Fatalf("UpdateFailoverState: %v", err)
	}
	got, _ := s.Failover(42)
	if got.FailCount != 2 || got.LastStatus != "no_balance" || got.LastDetail != "余额不足" ||
		got.LastCheckAt != "2026-01-01T00:00:00Z" || got.LastTaskID != "task-1" {
		t.Fatalf("状态更新错误: %+v", got)
	}

	// 空字符串只更新 fail_count/status/checkAt，其余保持。
	if err := s.UpdateFailoverState(42, 0, "ok", "连通", "2026-01-02T00:00:00Z", ""); err != nil {
		t.Fatalf("UpdateFailoverState 2: %v", err)
	}
	got, _ = s.Failover(42)
	if got.FailCount != 0 || got.LastStatus != "ok" || got.LastCheckAt != "2026-01-02T00:00:00Z" {
		t.Fatalf("状态更新错误: %+v", got)
	}
	if got.LastTaskID != "task-1" || got.LastDetail != "连通" {
		t.Fatalf("空字符串更新应保留原值: %+v", got)
	}
}

// TestSetFailoverPreservesReplacedBy 确认 SetFailover 不会清空 replaced_by。
func TestSetFailoverPreservesReplacedBy(t *testing.T) {
	s, _ := newTestStore(t)
	cfg := Failover{
		InstanceID: 42, UserID: 1, GroupID: 3, TokenID: 7, Enabled: true,
		Threshold: 3, IntervalSec: 300,
	}
	if err := s.SetFailover(cfg); err != nil {
		t.Fatalf("SetFailover: %v", err)
	}
	// 直接写入 replaced_by（模拟替换归档）。
	if _, err := s.db.Exec(`UPDATE instance_failover SET replaced_by=? WHERE instance_id=?`, 99, 42); err != nil {
		t.Fatalf("update replaced_by: %v", err)
	}

	cfg.Threshold = 9
	if err := s.SetFailover(cfg); err != nil {
		t.Fatalf("SetFailover update: %v", err)
	}
	got, _ := s.Failover(42)
	if got.ReplacedBy != 99 {
		t.Fatalf("replaced_by 被覆盖: %d", got.ReplacedBy)
	}
	if got.Threshold != 9 {
		t.Fatalf("设置项未更新: %+v", got)
	}
}

// TestFailoversOfGroup 验证按账号组过滤与升序排列。
func TestFailoversOfGroup(t *testing.T) {
	s, _ := newTestStore(t)
	mk := func(id, group int) Failover {
		return Failover{InstanceID: id, UserID: 1, GroupID: group, TokenID: 7, Enabled: true, Threshold: 3, IntervalSec: 300}
	}
	// 乱序写入，验证输出按 instance_id 升序。
	for _, f := range []Failover{mk(30, 2), mk(10, 1), mk(20, 1), mk(40, 2)} {
		if err := s.SetFailover(f); err != nil {
			t.Fatalf("SetFailover: %v", err)
		}
	}

	g1 := s.FailoversOfGroup(1)
	if len(g1) != 2 || g1[0].InstanceID != 10 || g1[1].InstanceID != 20 {
		t.Fatalf("FailoversOfGroup(1) = %+v", g1)
	}
	g2 := s.FailoversOfGroup(2)
	if len(g2) != 2 || g2[0].InstanceID != 30 || g2[1].InstanceID != 40 {
		t.Fatalf("FailoversOfGroup(2) = %+v", g2)
	}
	if got := s.FailoversOfGroup(999); len(got) != 0 {
		t.Fatalf("空组应返回空列表: %+v", got)
	}

	// Failovers 全局列表也按 instance_id 升序。
	all := s.Failovers()
	if len(all) != 4 || all[0].InstanceID != 10 || all[3].InstanceID != 40 {
		t.Fatalf("Failovers = %+v", all)
	}
}

// TestMoveFailover 验证配置跟随实例换 id，且监控状态被重置。
func TestMoveFailover(t *testing.T) {
	s, _ := newTestStore(t)
	cfg := Failover{
		InstanceID: 42, UserID: 1, GroupID: 3, TokenID: 7, Enabled: true,
		Threshold: 2, IntervalSec: 60,
	}
	if err := s.SetFailover(cfg); err != nil {
		t.Fatalf("SetFailover: %v", err)
	}
	_ = s.UpdateFailoverState(42, 3, "banned", "账号被封", "2026-01-01T00:00:00Z", "task-9")
	if _, err := s.db.Exec(`UPDATE instance_failover SET replaced_by=? WHERE instance_id=?`, 777, 42); err != nil {
		t.Fatalf("update replaced_by: %v", err)
	}

	if err := s.MoveFailover(42, 99); err != nil {
		t.Fatalf("MoveFailover: %v", err)
	}
	if _, ok := s.Failover(42); ok {
		t.Fatal("旧 id 应被删除")
	}
	got, ok := s.Failover(99)
	if !ok {
		t.Fatal("新 id 应携带配置")
	}
	if !got.Enabled || got.GroupID != 3 || got.TokenID != 7 || got.Threshold != 2 || got.IntervalSec != 60 {
		t.Fatalf("迁移后设置项丢失: %+v", got)
	}
	if got.FailCount != 0 || got.LastStatus != "" || got.LastDetail != "" || got.LastCheckAt != "" || got.LastTaskID != "" {
		t.Fatalf("迁移后监控状态应重置: %+v", got)
	}
	if got.ReplacedBy != 0 {
		t.Fatalf("迁移后 replaced_by 应重置为 0: %d", got.ReplacedBy)
	}

	// from == to 直接返回 nil，配置保持不变。
	if err := s.MoveFailover(99, 99); err != nil {
		t.Fatalf("MoveFailover same id: %v", err)
	}
	if _, ok := s.Failover(99); !ok {
		t.Fatal("同 id 移动不应删除配置")
	}

	// 源不存在是无操作，不报错。
	if err := s.MoveFailover(1234, 5678); err != nil {
		t.Fatalf("MoveFailover 源不存在: %v", err)
	}
	if _, ok := s.Failover(5678); ok {
		t.Fatal("源不存在时不应创建新配置")
	}
}

// TestFailoverEnabledRoundtrip 验证 enabled 布尔值往返。
func TestFailoverEnabledRoundtrip(t *testing.T) {
	s, _ := newTestStore(t)
	base := Failover{InstanceID: 1, UserID: 1, GroupID: 1, TokenID: 1, Threshold: 3, IntervalSec: 300}

	base.Enabled = false
	if err := s.SetFailover(base); err != nil {
		t.Fatalf("SetFailover false: %v", err)
	}
	if got, _ := s.Failover(1); got.Enabled {
		t.Fatal("enabled=false 往返错误")
	}

	base.Enabled = true
	if err := s.SetFailover(base); err != nil {
		t.Fatalf("SetFailover true: %v", err)
	}
	if got, _ := s.Failover(1); !got.Enabled {
		t.Fatal("enabled=true 往返错误")
	}
}
