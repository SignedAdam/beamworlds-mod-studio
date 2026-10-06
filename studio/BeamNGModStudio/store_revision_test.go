package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	modkit "github.com/SignedAdam/beamworlds-modkit"
)

func applyRevisionScan(tb testing.TB, store *Store, roots []string, archives []ScanArchive) []LibraryItem {
	tb.Helper()
	ctx := context.Background()
	scanID, err := store.BeginScan(ctx, roots)
	if err != nil {
		tb.Fatalf("begin revision scan: %v", err)
	}
	items, err := store.ApplyScanBatch(ctx, scanID, roots, nil, archives, len(archives), len(archives), 0)
	if err != nil {
		tb.Fatalf("apply revision scan: %v", err)
	}
	return items
}

func requireRevisionItems(tb testing.TB, items []LibraryItem) {
	tb.Helper()
	for _, item := range items {
		if strings.TrimSpace(item.Revision) == "" {
			tb.Fatalf("library item %q has no revision: %#v", item.EntityID, item)
		}
		payload, err := json.Marshal(item)
		if err != nil {
			tb.Fatalf("marshal library item %q: %v", item.EntityID, err)
		}
		var encoded map[string]any
		if err := json.Unmarshal(payload, &encoded); err != nil {
			tb.Fatalf("decode library item %q: %v", item.EntityID, err)
		}
		if got, ok := encoded["revision"].(string); !ok || got != item.Revision {
			tb.Fatalf("library item %q JSON revision = %#v, want %q", item.EntityID, encoded["revision"], item.Revision)
		}
	}
}

func TestLibraryItemRevisionTracksScanChanges(t *testing.T) {
	store, root := openLibraryStorage(t)
	ctx := context.Background()
	archives := libraryFixtureArchives(root, 2, 20_000)
	initial := applyRevisionScan(t, store, []string{root}, archives)
	if len(initial) != len(archives) {
		t.Fatalf("initial revision items = %d, want %d", len(initial), len(archives))
	}
	requireRevisionItems(t, initial)
	a, ok := libraryItemByPath(initial, archives[0].ArchivePath)
	if !ok {
		t.Fatal("initial item A was not returned")
	}
	b, ok := libraryItemByPath(initial, archives[1].ArchivePath)
	if !ok {
		t.Fatal("initial item B was not returned")
	}

	unchanged := []ScanArchive{
		{Root: root, ArchivePath: archives[0].ArchivePath, SizeBytes: archives[0].SizeBytes, Modified: archives[0].Modified, Reused: true},
		{Root: root, ArchivePath: archives[1].ArchivePath, SizeBytes: archives[1].SizeBytes, Modified: archives[1].Modified, Reused: true},
	}
	unchangedItems := applyRevisionScan(t, store, []string{root}, unchanged)
	requireRevisionItems(t, unchangedItems)
	unchangedA, err := store.GetLibraryItem(ctx, a.EntityID)
	if err != nil {
		t.Fatal(err)
	}
	unchangedB, err := store.GetLibraryItem(ctx, b.EntityID)
	if err != nil {
		t.Fatal(err)
	}
	if unchangedA.Revision != a.Revision || unchangedB.Revision != b.Revision {
		t.Fatalf("unchanged reused scan changed revisions: A %q->%q B %q->%q", a.Revision, unchangedA.Revision, b.Revision, unchangedB.Revision)
	}

	changedB := archives[1]
	changedB.SizeBytes++
	changedB.Modified = changedB.Modified.Add(time.Hour)
	changedB.Manifest.SizeBytes = changedB.SizeBytes
	changedB.Manifest.ModifiedAt = changedB.Modified
	changedB.Manifest.CentralFingerprint = "revision-test-b-changed-fingerprint"
	changedB.Manifest.FullSHA256 = "revision-test-b-changed-sha"
	changedB.Manifest.Title = "Revision Test B Changed"
	changedBItems := applyRevisionScan(t, store, nil, []ScanArchive{changedB})
	requireRevisionItems(t, changedBItems)
	afterBChangeA, err := store.GetLibraryItem(ctx, a.EntityID)
	if err != nil {
		t.Fatal(err)
	}
	afterBChangeB, err := store.GetLibraryItem(ctx, b.EntityID)
	if err != nil {
		t.Fatal(err)
	}
	if afterBChangeA.Revision != unchangedA.Revision {
		t.Fatalf("unrelated B scan changed A revision: %q->%q", unchangedA.Revision, afterBChangeA.Revision)
	}
	if afterBChangeB.Revision == unchangedB.Revision {
		t.Fatalf("changed B scan did not change B revision: %q", afterBChangeB.Revision)
	}

	changedA := archives[0]
	changedA.SizeBytes++
	changedA.Modified = changedA.Modified.Add(2 * time.Hour)
	changedA.Manifest.SizeBytes = changedA.SizeBytes
	changedA.Manifest.ModifiedAt = changedA.Modified
	changedA.Manifest.CentralFingerprint = "revision-test-a-changed-fingerprint"
	changedA.Manifest.FullSHA256 = "revision-test-a-changed-sha"
	changedA.Manifest.Title = "Revision Test A Changed"
	changedAItems := applyRevisionScan(t, store, nil, []ScanArchive{changedA})
	requireRevisionItems(t, changedAItems)
	afterAChangeA, err := store.GetLibraryItem(ctx, a.EntityID)
	if err != nil {
		t.Fatal(err)
	}
	afterAChangeB, err := store.GetLibraryItem(ctx, b.EntityID)
	if err != nil {
		t.Fatal(err)
	}
	if afterAChangeA.Revision == afterBChangeA.Revision {
		t.Fatalf("changed A scan did not change A revision: %q", afterAChangeA.Revision)
	}
	if afterAChangeB.Revision != afterBChangeB.Revision {
		t.Fatalf("changed A scan changed unrelated B revision: %q->%q", afterBChangeB.Revision, afterAChangeB.Revision)
	}

	removedB := changedB
	removedB.Reused = true
	removedB.Manifest = modkit.Manifest{}
	removedItems := applyRevisionScan(t, store, []string{root}, []ScanArchive{removedB})
	requireRevisionItems(t, removedItems)
	removedA, err := store.GetLibraryItem(ctx, a.EntityID)
	if err != nil {
		t.Fatal(err)
	}
	removedBItem, err := store.GetLibraryItem(ctx, b.EntityID)
	if err != nil {
		t.Fatal(err)
	}
	if removedA.Revision == afterAChangeA.Revision || removedA.Linked {
		t.Fatalf("removed A did not invalidate its item: before=%q after=%q linked=%v", afterAChangeA.Revision, removedA.Revision, removedA.Linked)
	}
	if removedBItem.Revision != afterAChangeB.Revision {
		t.Fatalf("removing A changed unrelated B revision: %q->%q", afterAChangeB.Revision, removedBItem.Revision)
	}

	replacement := changedA
	replacement.ArchivePath = filepath.Join(root, "replacement", filepath.Base(changedA.ArchivePath))
	replacement.Manifest.ArchivePath = replacement.ArchivePath
	replacementItems := applyRevisionScan(t, store, []string{root}, []ScanArchive{replacement, removedB})
	requireRevisionItems(t, replacementItems)
	replacedA, err := store.GetLibraryItem(ctx, a.EntityID)
	if err != nil {
		t.Fatal(err)
	}
	if replacedA.EntityID != a.EntityID || !replacedA.Linked || !strings.EqualFold(replacedA.ArchivePath, replacement.ArchivePath) || replacedA.Revision == removedA.Revision {
		t.Fatalf("replacement A did not preserve identity and invalidate revision: %#v", replacedA)
	}
}
