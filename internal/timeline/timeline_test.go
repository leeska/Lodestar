package timeline

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/leeska/Lodestar/internal/model"
	"github.com/leeska/Lodestar/internal/module"
	"github.com/leeska/Lodestar/internal/storage"
)

func TestListMergesEventsAuditAndEntries(t *testing.T) {
	m := &Module{}
	s, err := storage.OpenWith(filepath.Join(t.TempDir(), "t.db"), module.Migrations([]module.Module{m}))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	m.DB = s.DB()
	ctx := context.Background()
	now := time.Now().Unix()
	s.RecordEvent(ctx, model.Event{NodeID: "asset:x", NodeName: "域名 a", Kind: model.EventAsset, Message: "还有 7 天", Active: true, At: now - 30})
	s.Audit(ctx, model.AuditEntry{At: now - 20, Actor: "key:bot", Source: "1.2.3.4", Action: "POST /api/v1/admin/assets", Status: 200})
	m.Emit(ctx, Entry{At: now - 10, Source: "jobs", Kind: "job.failed", Level: Bad, Title: "资产到期提醒 失败", Ref: "job:assets.remind"})
	all, err := m.List(ctx, Query{})
	if err != nil || len(all) != 3 || all[0].Source != "jobs" || all[1].Source != "audit" || all[2].Source != "event" {
		t.Fatalf("merged: %+v %v", all, err)
	}
	if all[2].Title != "域名 a · 资产到期" || all[2].Level != Bad || all[1].Body != "key:bot" {
		t.Fatalf("shaping: %+v", all)
	}
	only, _ := m.List(ctx, Query{Sources: []string{"modules"}})
	if len(only) != 1 || only[0].Source != "jobs" {
		t.Fatalf("modules only: %+v", only)
	}
	page, _ := m.List(ctx, Query{Before: now - 15})
	if len(page) != 2 {
		t.Fatalf("before: %+v", page)
	}
	ref, _ := m.List(ctx, Query{Ref: "asset:x"})
	if len(ref) != 1 || ref[0].Source != "event" {
		t.Fatalf("ref: %+v", ref)
	}
}
