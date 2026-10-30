package store

import (
	"testing"
)

// TestAutoRebuildCRUD covers upsert/read/list/delete for the per-instance
// auto-rebuild config.
func TestAutoRebuildCRUD(t *testing.T) {
	s, _ := newTestStore(t)

	if _, ok := s.AutoRebuild(42); ok {
		t.Fatal("unconfigured instance should not be found")
	}

	cfg := AutoRebuild{
		InstanceID:  42,
		UserID:      1,
		TokenID:     7,
		Enabled:     true,
		Port:        22,
		Threshold:   3,
		IntervalSec: 300,
	}
	if err := s.SetAutoRebuild(cfg); err != nil {
		t.Fatalf("SetAutoRebuild: %v", err)
	}
	got, ok := s.AutoRebuild(42)
	if !ok {
		t.Fatal("AutoRebuild not found after set")
	}
	if !got.Enabled || got.Port != 22 || got.Threshold != 3 || got.IntervalSec != 300 || got.TokenID != 7 || got.UserID != 1 {
		t.Fatalf("got = %+v", got)
	}
	if got.UpdatedAt == "" {
		t.Fatal("UpdatedAt not set")
	}

	// Editing settings preserves monitor state.
	if err := s.UpdateAutoRebuildState(42, 2, "blocked", "端口不通", "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z", "task-1"); err != nil {
		t.Fatalf("UpdateAutoRebuildState: %v", err)
	}
	cfg.Threshold = 5
	cfg.Port = 443
	if err := s.SetAutoRebuild(cfg); err != nil {
		t.Fatalf("SetAutoRebuild update: %v", err)
	}
	got, _ = s.AutoRebuild(42)
	if got.Threshold != 5 || got.Port != 443 {
		t.Fatalf("settings not updated: %+v", got)
	}
	if got.FailCount != 2 || got.LastStatus != "blocked" || got.LastTaskID != "task-1" {
		t.Fatalf("state not preserved: %+v", got)
	}

	// State-only update with empty strings keeps prior values.
	if err := s.UpdateAutoRebuildState(42, 0, "ok", "连通", "2026-01-02T00:00:00Z", "", ""); err != nil {
		t.Fatalf("UpdateAutoRebuildState 2: %v", err)
	}
	got, _ = s.AutoRebuild(42)
	if got.FailCount != 0 || got.LastStatus != "ok" || got.LastCheckAt != "2026-01-02T00:00:00Z" {
		t.Fatalf("state update wrong: %+v", got)
	}
	if got.LastTaskID != "task-1" || got.LastBlockedAt != "2026-01-01T00:00:00Z" {
		t.Fatalf("empty-string update should keep prior values: %+v", got)
	}

	list := s.AutoRebuilds()
	if len(list) != 1 || list[0].InstanceID != 42 {
		t.Fatalf("AutoRebuilds = %+v", list)
	}

	if err := s.DeleteAutoRebuild(42); err != nil {
		t.Fatalf("DeleteAutoRebuild: %v", err)
	}
	if _, ok := s.AutoRebuild(42); ok {
		t.Fatal("config should be gone after delete")
	}
}

// TestMoveAutoRebuild verifies an instance's config follows it to a new id and
// that monitor state is reset.
func TestMoveAutoRebuild(t *testing.T) {
	s, _ := newTestStore(t)
	cfg := AutoRebuild{
		InstanceID: 42, UserID: 1, TokenID: 7, Enabled: true,
		Port: 2222, Threshold: 2, IntervalSec: 60,
	}
	if err := s.SetAutoRebuild(cfg); err != nil {
		t.Fatalf("SetAutoRebuild: %v", err)
	}
	_ = s.UpdateAutoRebuildState(42, 3, "blocked", "x", "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z", "task-9")

	if err := s.MoveAutoRebuild(42, 99); err != nil {
		t.Fatalf("MoveAutoRebuild: %v", err)
	}
	if _, ok := s.AutoRebuild(42); ok {
		t.Fatal("old id should be gone")
	}
	got, ok := s.AutoRebuild(99)
	if !ok {
		t.Fatal("new id should carry the config")
	}
	if !got.Enabled || got.Port != 2222 || got.Threshold != 2 || got.IntervalSec != 60 || got.TokenID != 7 {
		t.Fatalf("settings lost in move: %+v", got)
	}
	if got.FailCount != 0 || got.LastStatus != "" || got.LastBlockedAt != "" || got.LastTaskID != "" {
		t.Fatalf("state should reset on move: %+v", got)
	}

	// Moving a missing config is a no-op, not an error.
	if err := s.MoveAutoRebuild(1234, 5678); err != nil {
		t.Fatalf("MoveAutoRebuild missing: %v", err)
	}
}
