package storage

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/northplane/northplane/internal/model"
)

func TestReportArchiveCRUD(t *testing.T) {
	matrix(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		tenant := model.DefaultTenant
		now := time.Date(2026, 6, 7, 7, 0, 0, 0, time.UTC)

		// no slot yet
		if has, err := s.HasReportArchiveSlot(ctx, tenant, "sla", "2026-06"); err != nil || has {
			t.Fatalf("HasReportArchiveSlot empty: has=%v err=%v", has, err)
		}

		e := &ReportArchiveEntry{
			TenantID: tenant, ReportName: "sla", Slot: "2026-06",
			Format: "html", Content: []byte("<!doctype html><html>hi</html>"), CreatedAt: now,
		}
		if err := s.PutReportArchive(ctx, e, 0); err != nil {
			t.Fatalf("put: %v", err)
		}
		if e.ID == "" {
			t.Fatal("put did not assign an id")
		}

		has, err := s.HasReportArchiveSlot(ctx, tenant, "sla", "2026-06")
		if err != nil || !has {
			t.Fatalf("HasReportArchiveSlot after put: has=%v err=%v", has, err)
		}
		// different report / slot must not collide
		if has, _ := s.HasReportArchiveSlot(ctx, tenant, "sla", "2026-05"); has {
			t.Fatal("slot 2026-05 should not exist")
		}
		if has, _ := s.HasReportArchiveSlot(ctx, tenant, "other", "2026-06"); has {
			t.Fatal("report 'other' should not exist")
		}

		list, err := s.ListReportArchive(ctx, tenant, "sla", 10)
		if err != nil || len(list) != 1 {
			t.Fatalf("list: n=%d err=%v", len(list), err)
		}
		if list[0].Content != nil {
			t.Fatal("list must not return content")
		}
		if list[0].Format != "html" || list[0].Slot != "2026-06" {
			t.Fatalf("list entry wrong: %+v", list[0])
		}

		got, err := s.GetReportArchive(ctx, tenant, e.ID)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if string(got.Content) != string(e.Content) {
			t.Fatalf("content mismatch: %q", got.Content)
		}

		// wrong-tenant fetch must 404
		if _, err := s.GetReportArchive(ctx, "no-such-tenant", e.ID); err != ErrNotFound {
			t.Fatalf("cross-tenant get: want ErrNotFound, got %v", err)
		}
		if _, err := s.GetReportArchive(ctx, tenant, "no-such-id"); err != ErrNotFound {
			t.Fatalf("missing get: want ErrNotFound, got %v", err)
		}
	})
}

func TestReportArchiveKeepPruning(t *testing.T) {
	matrix(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		tenant := model.DefaultTenant
		base := time.Date(2026, 1, 1, 7, 0, 0, 0, time.UTC)
		keep := 3

		// 6 daily slots, each with HTML + CSV (2 rows per slot).
		for i := 0; i < 6; i++ {
			day := base.AddDate(0, 0, i)
			slot := day.Format("2006-01-02")
			for _, f := range []string{"html", "csv"} {
				if err := s.PutReportArchive(ctx, &ReportArchiveEntry{
					TenantID: tenant, ReportName: "daily-rep", Slot: slot,
					Format: f, Content: []byte(fmt.Sprintf("%s-%s", slot, f)), CreatedAt: day,
				}, keep); err != nil {
					t.Fatalf("put %s/%s: %v", slot, f, err)
				}
			}
		}

		list, err := s.ListReportArchive(ctx, tenant, "daily-rep", 100)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		// keep=3 slots × 2 formats = 6 rows survive.
		if len(list) != keep*2 {
			t.Fatalf("want %d rows after pruning, got %d", keep*2, len(list))
		}
		// the 3 newest slots survive; the 3 oldest are gone.
		seen := map[string]int{}
		for _, e := range list {
			seen[e.Slot]++
		}
		for i := 0; i < 3; i++ {
			old := base.AddDate(0, 0, i).Format("2006-01-02")
			if seen[old] != 0 {
				t.Fatalf("old slot %s should have been pruned", old)
			}
		}
		for i := 3; i < 6; i++ {
			keepSlot := base.AddDate(0, 0, i).Format("2006-01-02")
			if seen[keepSlot] != 2 {
				t.Fatalf("kept slot %s should have 2 rows, got %d", keepSlot, seen[keepSlot])
			}
		}

		// newest-first ordering
		if list[0].Slot != base.AddDate(0, 0, 5).Format("2006-01-02") {
			t.Fatalf("list not newest-first: head slot %s", list[0].Slot)
		}

		// re-archiving the same slot must not grow the archive beyond keep.
		newest := base.AddDate(0, 0, 5).Format("2006-01-02")
		if err := s.PutReportArchive(ctx, &ReportArchiveEntry{
			TenantID: tenant, ReportName: "daily-rep", Slot: newest,
			Format: "json", Content: []byte("again"), CreatedAt: base.AddDate(0, 0, 5),
		}, keep); err != nil {
			t.Fatalf("re-put: %v", err)
		}
		list2, _ := s.ListReportArchive(ctx, tenant, "daily-rep", 100)
		distinct := map[string]bool{}
		for _, e := range list2 {
			distinct[e.Slot] = true
		}
		if len(distinct) > keep {
			t.Fatalf("distinct slots %d exceeds keep %d", len(distinct), keep)
		}
	})
}

func TestReportArchiveDefaultKeep(t *testing.T) {
	matrix(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		tenant := model.DefaultTenant
		base := time.Date(2026, 1, 1, 7, 0, 0, 0, time.UTC)

		// keep<=0 ⇒ DefaultReportKeep (12). Write 15 daily slots.
		for i := 0; i < 15; i++ {
			day := base.AddDate(0, 0, i)
			if err := s.PutReportArchive(ctx, &ReportArchiveEntry{
				TenantID: tenant, ReportName: "rep", Slot: day.Format("2006-01-02"),
				Format: "html", Content: []byte("x"), CreatedAt: day,
			}, 0); err != nil {
				t.Fatalf("put %d: %v", i, err)
			}
		}
		list, _ := s.ListReportArchive(ctx, tenant, "rep", 100)
		if len(list) != DefaultReportKeep {
			t.Fatalf("default keep: want %d rows, got %d", DefaultReportKeep, len(list))
		}
	})
}
