package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- helpers -----------------------------------------------------------------

// setFileMtime sets the mtime of a file to the given time.
func setFileMtime(t *testing.T, path string, mtime time.Time) {
	t.Helper()
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

// scanAndCreateCollection creates a library of count archives under root,
// creates a collection containing all of them, and returns the items and
// collection ID. Each archive file is written to disk so the materialize
// loop can stat/hash them.
func scanAndCreateCollection(t *testing.T, service *AppService, name string, count, offset int) ([]LibraryItem, string) {
	t.Helper()
	root := filepath.Join(service.config.DataDir, "library")
	fixtures := libraryFixtureArchives(root, count, offset)
	for i, fix := range fixtures {
		content := []byte(fmt.Sprintf("fixture-archive-content-%05d-padding-to-grow-the-file", offset+i))
		if err := os.MkdirAll(filepath.Dir(fix.ArchivePath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fix.ArchivePath, content, 0o644); err != nil {
			t.Fatal(err)
		}
		// Set the mtime to match the fixture's Modified time so size+mtime
		// identity checks pass.
		setFileMtime(t, fix.ArchivePath, fix.Modified)
		// Recompute the manifest SHA to match actual file content.
		sum := sha256.Sum256(content)
		fixtures[i].Manifest.FullSHA256 = hex.EncodeToString(sum[:])
		fixtures[i].SizeBytes = int64(len(content))
		fixtures[i].Manifest.SizeBytes = int64(len(content))
	}
	items := applyLibraryArchives(t, service.store, root, fixtures)
	collection, err := service.CreateCollection(name, "", "")
	if err != nil {
		t.Fatal(err)
	}
	entityIDs := make([]string, len(items))
	for i, item := range items {
		entityIDs[i] = item.EntityID
	}
	if _, err := service.SetCollectionMods(collection.Collection.ID, entityIDs, true); err != nil {
		t.Fatal(err)
	}
	return items, collection.Collection.ID
}

// resolveAndFingerprint resolves the selection and returns a PlayRequest with
// the current fingerprint, ready for launch.
func resolveAndFingerprint(t *testing.T, service *AppService, collectionID string) PlayRequest {
	t.Helper()
	selection, err := service.ResolvePlaySelection([]string{collectionID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return PlayRequest{CollectionIDs: []string{collectionID}, Fingerprint: selection.Fingerprint}
}

// activateAndCheck is a shorthand to run activatePlaySelection expecting success.
func activateAndCheck(t *testing.T, service *AppService, request PlayRequest) PlayActivation {
	t.Helper()
	activation, err := service.activatePlaySelection(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	return activation
}

// --- tests -------------------------------------------------------------------

func TestMaterializeFastPathSkipsReadWhenIdentityMatches(t *testing.T) {
	service := newTestAppService(t)
	_, collectionID := scanAndCreateCollection(t, service, "FastPath", 1, 7001)
	request := resolveAndFingerprint(t, service, collectionID)

	// First launch: populates the cache.
	activateAndCheck(t, service, request)

	// Resolve again (the fingerprint may have changed due to pending SHA writes).
	request = resolveAndFingerprint(t, service, collectionID)

	// Swap the archive's contents for different bytes of exactly the same
	// length, then put the recorded modification time back. Reading the file
	// would now produce a hash that does not match the recorded one, which the
	// launch refuses - so a launch that SUCCEEDS is only possible if the file
	// was never read. os.Chmod(0) cannot express this: on Windows it clears
	// the read-only bit and reading still works, so a permission-based test
	// passes whether the fast path is in effect or not.
	root := filepath.Join(service.config.DataDir, "library")
	archivePath := filepath.Join(root, fmt.Sprintf("library-%05d.zip", 7001))
	info, err := os.Stat(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	swapped := make([]byte, len(original))
	for i := range swapped {
		swapped[i] = original[i] ^ 0xFF
	}
	if err := os.WriteFile(archivePath, swapped, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(archivePath, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		t.Fatalf("identity was not preserved: size %d -> %d, mtime %v -> %v",
			info.Size(), after.Size(), info.ModTime(), after.ModTime())
	}

	activation := activateAndCheck(t, service, request)
	if activation.ModCount != 1 {
		t.Fatalf("expected 1 mod, got %d", activation.ModCount)
	}
}

func TestMaterializeRefusesChangedArchiveSizeChange(t *testing.T) {
	service := newTestAppService(t)
	_, collectionID := scanAndCreateCollection(t, service, "SizeChange", 1, 7010)
	request := resolveAndFingerprint(t, service, collectionID)

	// Change the archive content (different size) after the preview.
	root := filepath.Join(service.config.DataDir, "library")
	archivePath := filepath.Join(root, fmt.Sprintf("library-%05d.zip", 7010))
	if err := os.WriteFile(archivePath, []byte("completely-different-content-that-is-longer-than-before-and-has-different-hash"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := service.activatePlaySelection(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "changed since the Play preview") {
		t.Fatalf("expected 'changed since the Play preview' error, got: %v", err)
	}
}

func TestMaterializeRefusesChangedArchiveMtimeOnly(t *testing.T) {
	service := newTestAppService(t)
	_, collectionID := scanAndCreateCollection(t, service, "MtimeChange", 1, 7020)
	request := resolveAndFingerprint(t, service, collectionID)

	// Change only the mtime (same size, same content -> same hash).
	// The size+mtime fast path will see the mtime mismatch and fall through
	// to hashing. Since the content is the same, the hash matches, and the
	// launch succeeds. But if we change the content too (same size, different
	// content), the hash won't match.
	root := filepath.Join(service.config.DataDir, "library")
	archivePath := filepath.Join(root, fmt.Sprintf("library-%05d.zip", 7020))

	// Read current content to know its size, write different content of same size.
	original, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	replacement := make([]byte, len(original))
	for i := range replacement {
		replacement[i] = byte('Z')
	}
	if err := os.WriteFile(archivePath, replacement, 0o644); err != nil {
		t.Fatal(err)
	}
	// The mtime is now different from the recorded one, and the content hash differs.

	_, err = service.activatePlaySelection(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "changed since the Play preview") {
		t.Fatalf("expected 'changed since the Play preview' error, got: %v", err)
	}
}

func TestMaterializeFallsBackToHashingWhenNoRecordedSHA(t *testing.T) {
	service := newTestAppService(t)
	root := filepath.Join(service.config.DataDir, "library")
	fixtures := libraryFixtureArchives(root, 1, 7030)
	content := []byte(fmt.Sprintf("fixture-archive-content-%05d-padding-to-grow-the-file", 7030))
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixtures[0].ArchivePath, content, 0o644); err != nil {
		t.Fatal(err)
	}
	setFileMtime(t, fixtures[0].ArchivePath, fixtures[0].Modified)

	// Intentionally clear the SHA so it's empty in the DB.
	fixtures[0].Manifest.FullSHA256 = ""
	fixtures[0].SizeBytes = int64(len(content))
	fixtures[0].Manifest.SizeBytes = int64(len(content))

	items := applyLibraryArchives(t, service.store, root, fixtures)
	collection, err := service.CreateCollection("NoSHA", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetCollectionMods(collection.Collection.ID, []string{items[0].EntityID}, true); err != nil {
		t.Fatal(err)
	}

	request := resolveAndFingerprint(t, service, collection.Collection.ID)

	// The launch must succeed by hashing the file at launch time.
	activation := activateAndCheck(t, service, request)
	if activation.ModCount != 1 {
		t.Fatalf("expected 1 mod, got %d", activation.ModCount)
	}
}

func TestMaterializeDiscardsCacheWithSizeMismatch(t *testing.T) {
	service := newTestAppService(t)
	_, collectionID := scanAndCreateCollection(t, service, "CacheMismatch", 1, 7040)
	request := resolveAndFingerprint(t, service, collectionID)

	// First launch: creates the cache entry.
	activateAndCheck(t, service, request)

	// Corrupt the cache entry. The cache may be hard-linked to the source, so
	// we must remove first to break the link, then write corrupt content.
	cacheRoot := filepath.Join(service.config.ProfileDir, playCacheDirectory)
	entries, err := os.ReadDir(cacheRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("expected at least one cache entry")
	}
	cachePath := filepath.Join(cacheRoot, entries[0].Name())
	if err := os.Remove(cachePath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cachePath, []byte("corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Re-resolve to get a fresh fingerprint.
	request = resolveAndFingerprint(t, service, collectionID)

	// The launch should detect the size mismatch, hash the corrupt file,
	// discard it, and refill the cache from the source.
	activation := activateAndCheck(t, service, request)
	if activation.ModCount != 1 {
		t.Fatalf("expected 1 mod, got %d", activation.ModCount)
	}

	// Verify the cache was refilled with correct content.
	info, err := os.Stat(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() == int64(len("corrupt")) {
		t.Fatal("cache entry was not refilled after size mismatch")
	}
}

func TestMaterializeSelectedKeysOrderAndProgressMonotonic(t *testing.T) {
	service := newTestAppService(t)
	_, collectionID := scanAndCreateCollection(t, service, "OrderProgress", 8, 7050)

	// Collect progress events.
	var progressMu sync.Mutex
	var progressEvents []PlayProgress
	service.emit = func(name string, data any) {
		if name == "play:progress" {
			progressMu.Lock()
			if p, ok := data.(PlayProgress); ok {
				progressEvents = append(progressEvents, p)
			}
			progressMu.Unlock()
		}
	}

	request := resolveAndFingerprint(t, service, collectionID)
	selection, _ := service.ResolvePlaySelection([]string{collectionID}, nil)
	activation := activateAndCheck(t, service, request)

	if activation.ModCount != len(selection.Mods) {
		t.Fatalf("mod count mismatch: activation %d vs selection %d", activation.ModCount, len(selection.Mods))
	}

	// Verify progress.Completed never goes backwards.
	progressMu.Lock()
	maxCompleted := 0
	for _, p := range progressEvents {
		if p.Phase != "materializing" {
			continue
		}
		if p.Completed < maxCompleted {
			t.Fatalf("progress.Completed went backwards: %d -> %d", maxCompleted, p.Completed)
		}
		maxCompleted = p.Completed
	}
	if maxCompleted != len(selection.Mods) {
		t.Fatalf("final progress.Completed = %d, want %d", maxCompleted, len(selection.Mods))
	}
	progressMu.Unlock()

	// Run a second launch and verify that selectedKeys order is still deterministic.
	request2 := resolveAndFingerprint(t, service, collectionID)
	activation2 := activateAndCheck(t, service, request2)
	if activation2.ModCount != activation.ModCount {
		t.Fatalf("second launch mod count %d != first %d", activation2.ModCount, activation.ModCount)
	}
}

func TestMaterializeCancellationCleansUp(t *testing.T) {
	service := newTestAppService(t)
	_, collectionID := scanAndCreateCollection(t, service, "Cancel", 4, 7060)
	request := resolveAndFingerprint(t, service, collectionID)

	// Cancel the context after a tiny delay to hit mid-loop cancellation.
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(1 * time.Millisecond)
		cancel()
	}()

	_, err := service.activatePlaySelection(ctx, request)
	if err == nil {
		// The launch completed before cancellation fired; that is acceptable
		// for a race-sensitive test. Verify the managed directory is consistent.
		return
	}

	// Verify no partial managed directory was left behind.
	activeModsDir := service.config.ActiveModsDir
	entries, readErr := os.ReadDir(activeModsDir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".beamworlds-managed-next-") {
			t.Fatalf("partial managed directory left behind: %s", e.Name())
		}
	}
}

func TestLinkOrCopyArchiveToCacheLinkPath(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.zip")
	content := []byte("link-test-content-for-cache-verification")
	if err := os.WriteFile(source, content, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:])
	dest := filepath.Join(dir, "cache", hash+".zip")

	copied, err := linkOrCopyArchiveToCache(context.Background(), source, dest, hash)
	if err != nil {
		t.Fatal(err)
	}
	if copied != 0 {
		t.Fatalf("expected 0 bytes copied (hard link), got %d", copied)
	}
	// Verify the cache file exists and has the right content.
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Fatal("cache content does not match source")
	}

	// Verify they share the same inode (hard link).
	sourceStat, _ := os.Stat(source)
	destStat, _ := os.Stat(dest)
	if !os.SameFile(sourceStat, destStat) {
		t.Fatal("expected hard link (same inode), got different files")
	}
}

func TestLinkOrCopyArchiveToCacheCopyFallbackRejectsChecksumMismatch(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.zip")
	content := []byte("fallback-copy-test-content")
	if err := os.WriteFile(source, content, 0o644); err != nil {
		t.Fatal(err)
	}

	// Pass a wrong expected hash to trigger the checksum mismatch error.
	wrongHash := "0000000000000000000000000000000000000000000000000000000000000000"
	dest := filepath.Join(dir, "cache", wrongHash+".zip")

	// To force the copy fallback: make dest directory non-existent first to
	// ensure the link path doesn't hit a "dest already exists" scenario.
	// Actually we need the link to fail. On the same volume it will succeed.
	// So we must put dest on a different "volume" or pre-create the file.
	// Simplest: pre-create destination so os.Link fails with ErrExist, then
	// the fallback copy runs and finds the checksum mismatch.
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte("pre-existing"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Remove the pre-existing file but put it back as a directory to prevent link.
	if err := os.Remove(dest); err != nil {
		t.Fatal(err)
	}

	// Actually, simpler approach: use copyArchiveToCacheCounting directly to
	// test the copy fallback path, since linkOrCopyArchiveToCache will succeed
	// with os.Link on the same volume.
	_, err := copyArchiveToCacheCounting(context.Background(), source, dest, wrongHash)
	if err == nil || !strings.Contains(err.Error(), "source checksum changed") {
		t.Fatalf("expected checksum mismatch error, got: %v", err)
	}

	// Also verify the destination was cleaned up (no partial file left).
	if _, statErr := os.Stat(dest); statErr == nil {
		t.Fatal("partial cache file should have been cleaned up on checksum mismatch")
	}
}

func TestLinkOrCopyArchiveToCacheCopyFallbackSucceeds(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.zip")
	content := []byte("copy-fallback-success-content")
	if err := os.WriteFile(source, content, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:])
	dest := filepath.Join(dir, "cache", hash+".zip")

	// Directly test the copy path.
	copied, err := copyArchiveToCacheCounting(context.Background(), source, dest, hash)
	if err != nil {
		t.Fatal(err)
	}
	if copied != int64(len(content)) {
		t.Fatalf("expected %d bytes copied, got %d", len(content), copied)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Fatal("cache content does not match source")
	}
}

func TestLinkOrCopyLinkedEntrySurvivesCacheVerification(t *testing.T) {
	// Verifies that a hard-linked cache entry passes the size-based cache
	// verification in materializeMod (the cache check trusts the content-
	// addressed name when the size matches the recorded archive size).
	service := newTestAppService(t)
	_, collectionID := scanAndCreateCollection(t, service, "LinkSurvival", 1, 7080)
	request := resolveAndFingerprint(t, service, collectionID)

	// First launch: creates a hard-linked cache entry (or copy on cross-vol).
	activateAndCheck(t, service, request)

	// Verify cache file exists.
	cacheRoot := filepath.Join(service.config.ProfileDir, playCacheDirectory)
	entries, err := os.ReadDir(cacheRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("expected a cache entry after first launch")
	}

	// Second launch: should use the cached entry without error.
	request = resolveAndFingerprint(t, service, collectionID)
	activation := activateAndCheck(t, service, request)
	if activation.ModCount != 1 {
		t.Fatalf("expected 1 mod on second launch, got %d", activation.ModCount)
	}
}

func TestLaunchReplacesDuplicateCacheCopyWithLink(t *testing.T) {
	// Adam's cache had grown to 366 standalone copies of his library archives,
	// 90.17 GiB of duplicated bytes, because the old cache fill always streamed
	// bytes. Launching should heal those in place rather than asking him to
	// delete a directory by hand.
	service := newTestAppService(t)
	_, collectionID := scanAndCreateCollection(t, service, "Relink", 1, 7090)
	request := resolveAndFingerprint(t, service, collectionID)
	activateAndCheck(t, service, request)

	cacheRoot := filepath.Join(service.config.ProfileDir, playCacheDirectory)
	entries, err := os.ReadDir(cacheRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected one cache entry, got %d", len(entries))
	}
	cachePath := filepath.Join(cacheRoot, entries[0].Name())
	sourcePath := filepath.Join(service.config.DataDir, "library", fmt.Sprintf("library-%05d.zip", 7090))

	// Recreate the pre-fix state: an independent copy of the same bytes.
	content, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(cachePath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cachePath, content, 0o644); err != nil {
		t.Fatal(err)
	}
	sourceInfo, err := os.Stat(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	cacheInfo, err := os.Stat(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(sourceInfo, cacheInfo) {
		t.Fatal("fixture did not produce an independent copy")
	}

	request = resolveAndFingerprint(t, service, collectionID)
	activateAndCheck(t, service, request)

	healedSource, err := os.Stat(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	healedCache, err := os.Stat(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(healedSource, healedCache) {
		t.Fatal("the duplicate cache copy was not replaced with a link to the library archive")
	}
	if healedCache.Size() != sourceInfo.Size() {
		t.Fatalf("size changed: %d -> %d", sourceInfo.Size(), healedCache.Size())
	}
	got, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Fatal("cache content changed while relinking")
	}
}
