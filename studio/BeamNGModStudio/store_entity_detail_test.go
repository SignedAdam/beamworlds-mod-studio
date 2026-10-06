package main

import (
	"context"
	"testing"
)

func TestGetEntityDetailReportsHistoryTotalBeyondSlice(t *testing.T) {
	store, root := openLibraryStorage(t)
	ctx := context.Background()
	items := applyLibraryArchives(t, store, root, libraryFixtureArchives(root, 1, 0))
	if len(items) != 1 {
		t.Fatalf("fixture items = %d, want 1", len(items))
	}

	before, err := store.GetEntityDetail(ctx, items[0].EntityID)
	if err != nil {
		t.Fatalf("get initial entity detail: %v", err)
	}
	for index := range 125 {
		if err := store.AppendEvent(ctx, items[0].EntityID, "history_test_event", map[string]any{"sequence": index}); err != nil {
			t.Fatalf("append history event %d: %v", index, err)
		}
	}

	detail, err := store.GetEntityDetail(ctx, items[0].EntityID)
	if err != nil {
		t.Fatalf("get entity detail: %v", err)
	}
	if len(detail.History) != 100 {
		t.Fatalf("history slice length = %d, want capped length 100", len(detail.History))
	}
	wantTotal := len(before.History) + 125
	if detail.HistoryTotal != wantTotal {
		t.Fatalf("history total = %d, want %d", detail.HistoryTotal, wantTotal)
	}
}
