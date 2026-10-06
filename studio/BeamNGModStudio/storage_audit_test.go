package main

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func writeTestArchive(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
}

func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// writeValidModArchive creates a real valid BeamNG ZIP archive that will pass
// modkit.Inspect and modkit.FullSHA256, suitable for importOneMod.
func writeValidModArchive(t *testing.T, path, title string) []byte {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	metadata, err := writer.Create("mod_info/info.json")
	if err != nil {
		t.Fatal(err)
	}
	_, err = metadata.Write([]byte(fmt.Sprintf(`{"title":%q,"author":"Recovery Fixture","version":"1.0"}`, title)))
	if err != nil {
		t.Fatal(err)
	}
	member, err := writer.Create("vehicles/recovery_fixture/main.jbeam")
	if err != nil {
		t.Fatal(err)
	}
	_, err = member.Write([]byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// ---------------------------------------------------------------------------
// Audit tests
// ---------------------------------------------------------------------------

func TestAuditIdentifiesCanonicalSources(t *testing.T) {
	service := newTestAppService(t)
	root := filepath.Join(service.config.DataDir, "library")
	archives := []ScanArchive{
		modFamilyScanArchive(t, root, "source-mod.zip", "Source Mod", "Author", "1.0", "", "source-sha", "source-fp", 100, testArchiveModified(1), 0),
	}
	applyLibraryArchives(t, service.store, root, archives)

	audit, err := service.AuditArchiveStorage(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	var found bool
	for _, item := range audit.Items {
		if item.Classification == classCanonicalSource && strings.HasSuffix(item.Path, "source-mod.zip") {
			found = true
			if item.CleanupAllowed {
				t.Error("canonical source must not be cleanup-allowed")
			}
			if item.EntityID == "" {
				t.Error("canonical source must have an entity ID")
			}
		}
	}
	if !found {
		t.Errorf("canonical source not found in audit; items = %d", len(audit.Items))
	}
}

func TestAuditClassifiesLegacyCacheRedundant(t *testing.T) {
	service := newTestAppService(t)
	root := filepath.Join(service.config.DataDir, "library")

	content := []byte("redundant-cache-test-content-padding-to-100-bytes" + strings.Repeat("x", 50))
	contentSHA := sha256Hex(content)

	sourcePath := filepath.Join(root, "cached-mod.zip")
	writeTestArchive(t, sourcePath, content)

	archives := []ScanArchive{
		modFamilyScanArchive(t, root, "cached-mod.zip", "Cached Mod", "Author", "1.0", "", contentSHA, "cached-fp", int64(len(content)), testArchiveModified(2), 0),
	}
	writeTestArchive(t, sourcePath, content)
	applyLibraryArchives(t, service.store, root, archives)

	cacheDir := filepath.Join(service.config.ProfileDir, legacyArchiveCacheDirectory)
	cachePath := filepath.Join(cacheDir, contentSHA+".zip")
	writeTestArchive(t, cachePath, content)

	audit, err := service.AuditArchiveStorage(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	var cacheItem *StorageAuditItem
	for i := range audit.Items {
		if audit.Items[i].Classification == classLegacyCacheRedundant {
			cacheItem = &audit.Items[i]
			break
		}
	}
	if cacheItem == nil {
		t.Fatalf("no redundant cache item found; items = %d", len(audit.Items))
	}
	if !cacheItem.CleanupAllowed {
		t.Error("redundant cache should be cleanup-allowed")
	}
	if cacheItem.EntityID == "" {
		t.Error("redundant cache should reference the source entity")
	}
}

func TestAuditClassifiesLegacyCacheOnly(t *testing.T) {
	service := newTestAppService(t)

	cacheDir := filepath.Join(service.config.ProfileDir, legacyArchiveCacheDirectory)
	content := []byte("orphan-cache-archive" + strings.Repeat("y", 80))
	orphanSHA := sha256Hex(content)
	cachePath := filepath.Join(cacheDir, orphanSHA+".zip")
	writeTestArchive(t, cachePath, content)

	audit, err := service.AuditArchiveStorage(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	var cacheOnlyItem *StorageAuditItem
	for i := range audit.Items {
		if audit.Items[i].Classification == classLegacyCacheOnly {
			cacheOnlyItem = &audit.Items[i]
			break
		}
	}
	if cacheOnlyItem == nil {
		t.Fatalf("no cache-only item found; items = %d", len(audit.Items))
	}
	if cacheOnlyItem.CleanupAllowed {
		t.Error("cache-only item must NOT be cleanup-allowed")
	}
	if !cacheOnlyItem.RecoveryAllowed {
		t.Error("cache-only item should be recovery-allowed")
	}
}

func TestAuditCacheWithMissingSourceClassifiedAsRetained(t *testing.T) {
	service := newTestAppService(t)
	root := filepath.Join(service.config.DataDir, "library")

	content := []byte("missing-source-test-content" + strings.Repeat("m", 73))
	contentSHA := sha256Hex(content)

	// Index a source then delete the file.
	sourcePath := filepath.Join(root, "missing-source.zip")
	writeTestArchive(t, sourcePath, content)
	archives := []ScanArchive{
		modFamilyScanArchive(t, root, "missing-source.zip", "Missing Source", "Author", "1.0", "", contentSHA, "missing-fp", int64(len(content)), testArchiveModified(3), 0),
	}
	writeTestArchive(t, sourcePath, content)
	applyLibraryArchives(t, service.store, root, archives)
	os.Remove(sourcePath)

	// Write a cache copy — source is gone so it's the sole survivor.
	cacheDir := filepath.Join(service.config.ProfileDir, legacyArchiveCacheDirectory)
	cachePath := filepath.Join(cacheDir, contentSHA+".zip")
	writeTestArchive(t, cachePath, content)

	audit, err := service.AuditArchiveStorage(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	var item *StorageAuditItem
	for i := range audit.Items {
		if strings.Contains(audit.Items[i].Path, contentSHA) {
			item = &audit.Items[i]
			break
		}
	}
	if item == nil {
		t.Fatal("cache item not found")
	}
	// With source missing, this should be classified as cache-only/retained,
	// NOT as redundant+cleanable.
	if item.Classification != classLegacyCacheOnly {
		t.Errorf("classification = %s, want %s (source missing)", item.Classification, classLegacyCacheOnly)
	}
	if item.CleanupAllowed {
		t.Error("cache item with missing source must NOT be cleanup-allowed")
	}
	if !item.RecoveryAllowed {
		t.Error("cache item with missing source should be recovery-allowed")
	}
}

// ---------------------------------------------------------------------------
// Stable fingerprint tests
// ---------------------------------------------------------------------------

func TestAuditFingerprintStableAcrossReAudits(t *testing.T) {
	service := newTestAppService(t)
	root := filepath.Join(service.config.DataDir, "library")
	archives := []ScanArchive{
		modFamilyScanArchive(t, root, "stable-fp.zip", "Stable FP", "Author", "1.0", "", "stable-sha", "stable-fp", 100, testArchiveModified(1), 0),
	}
	applyLibraryArchives(t, service.store, root, archives)

	audit1, err := service.AuditArchiveStorage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	audit2, err := service.AuditArchiveStorage(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if audit1.Fingerprint != audit2.Fingerprint {
		t.Errorf("fingerprints differ across identical re-audits:\n  first:  %s\n  second: %s", audit1.Fingerprint, audit2.Fingerprint)
	}

	// Item IDs should also be stable.
	if len(audit1.Items) != len(audit2.Items) {
		t.Fatalf("item counts differ: %d vs %d", len(audit1.Items), len(audit2.Items))
	}
	for i := range audit1.Items {
		if audit1.Items[i].ID != audit2.Items[i].ID {
			t.Errorf("item[%d] ID changed: %s → %s", i, audit1.Items[i].ID, audit2.Items[i].ID)
		}
	}
}

func TestAuditFingerprintChangesWhenFilesChange(t *testing.T) {
	service := newTestAppService(t)
	root := filepath.Join(service.config.DataDir, "library")
	archives := []ScanArchive{
		modFamilyScanArchive(t, root, "fp-test.zip", "FP Mod", "Author", "1.0", "", "fp-sha", "fp-fp", 100, testArchiveModified(1), 0),
	}
	applyLibraryArchives(t, service.store, root, archives)

	audit1, err := service.AuditArchiveStorage(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	archives2 := []ScanArchive{
		modFamilyScanArchive(t, root, "fp-test-2.zip", "FP Mod 2", "Author", "2.0", "", "fp-sha-2", "fp-fp-2", 200, testArchiveModified(3), 0),
	}
	applyLibraryArchives(t, service.store, root, archives2)

	audit2, err := service.AuditArchiveStorage(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if audit1.Fingerprint == audit2.Fingerprint {
		t.Error("fingerprint should change when catalog content changes")
	}
}

// ---------------------------------------------------------------------------
// Cleanup safety tests
// ---------------------------------------------------------------------------

func TestCleanupRejectsStaleFingerprint(t *testing.T) {
	service := newTestAppService(t)

	_, err := service.ApplyStorageCleanup(context.Background(), "stale-fingerprint", []string{"item-1"})
	if err == nil {
		t.Fatal("expected error for stale fingerprint")
	}
	if !strings.Contains(err.Error(), "changed since") {
		t.Errorf("error = %q, want stale-review message", err.Error())
	}
}

func TestCleanupRefusesSoleSurvivingCopy(t *testing.T) {
	service := newTestAppService(t)

	cacheDir := filepath.Join(service.config.ProfileDir, legacyArchiveCacheDirectory)
	content := []byte("sole-surviving-archive" + strings.Repeat("z", 78))
	soleSHA := sha256Hex(content)
	writeTestArchive(t, filepath.Join(cacheDir, soleSHA+".zip"), content)

	audit, err := service.AuditArchiveStorage(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	var soleItem *StorageAuditItem
	for i := range audit.Items {
		if audit.Items[i].Classification == classLegacyCacheOnly {
			soleItem = &audit.Items[i]
			break
		}
	}
	if soleItem == nil {
		t.Fatal("no cache-only item found")
	}

	result, err := service.ApplyStorageCleanup(context.Background(), audit.Fingerprint, []string{soleItem.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Failures) == 0 {
		t.Error("expected a failure for sole surviving copy")
	}
	if _, statErr := os.Stat(soleItem.Path); statErr != nil {
		t.Errorf("sole copy was deleted: %v", statErr)
	}
}

func TestCleanupVerifiesSHA256BeforeRemovingCopy(t *testing.T) {
	service := newTestAppService(t)
	root := filepath.Join(service.config.DataDir, "library")

	sourceContent := []byte("source-content-for-sha-test" + strings.Repeat("a", 73))
	sourceSHA := sha256Hex(sourceContent)

	sourcePath := filepath.Join(root, "sha-verify.zip")
	writeTestArchive(t, sourcePath, sourceContent)
	archives := []ScanArchive{
		modFamilyScanArchive(t, root, "sha-verify.zip", "SHA Verify", "Author", "1.0", "", sourceSHA, "sha-fp", int64(len(sourceContent)), testArchiveModified(4), 0),
	}
	writeTestArchive(t, sourcePath, sourceContent)
	applyLibraryArchives(t, service.store, root, archives)

	// Write a cache entry with matching SHA name but DIFFERENT content.
	cacheDir := filepath.Join(service.config.ProfileDir, legacyArchiveCacheDirectory)
	corruptContent := []byte("CORRUPT-different-bytes-not-matching" + strings.Repeat("b", 64))
	cachePath := filepath.Join(cacheDir, sourceSHA+".zip")
	writeTestArchive(t, cachePath, corruptContent)

	audit, err := service.AuditArchiveStorage(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	var cacheItem *StorageAuditItem
	for i := range audit.Items {
		if samePath(audit.Items[i].Path,cachePath) {
			cacheItem = &audit.Items[i]
			break
		}
	}
	if cacheItem == nil {
		t.Fatal("cache archive was omitted from the review")
	}
	if cacheItem.CleanupAllowed { t.Fatal("a hash-looking filename made different bytes eligible for cleanup") }

	result, err := service.ApplyStorageCleanup(context.Background(), audit.Fingerprint, []string{cacheItem.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Failures) == 0 {
		t.Error("expected SHA256 mismatch failure")
	}
	if result.RemovedCopies != 0 {
		t.Error("should not have removed the mismatched copy")
	}
	if _, statErr := os.Stat(cachePath); statErr != nil {
		t.Errorf("mismatched cache copy was deleted: %v", statErr)
	}
}

func TestCleanupSucceedsForVerifiedRedundantCopy(t *testing.T) {
	service := newTestAppService(t)
	root := filepath.Join(service.config.DataDir, "library")

	content := []byte("verified-redundant-content" + strings.Repeat("c", 74))
	contentSHA := sha256Hex(content)

	sourcePath := filepath.Join(root, "verified.zip")
	writeTestArchive(t, sourcePath, content)
	archives := []ScanArchive{
		modFamilyScanArchive(t, root, "verified.zip", "Verified Mod", "Author", "1.0", "", contentSHA, "ver-fp", int64(len(content)), testArchiveModified(5), 0),
	}
	writeTestArchive(t, sourcePath, content)
	applyLibraryArchives(t, service.store, root, archives)

	cacheDir := filepath.Join(service.config.ProfileDir, legacyArchiveCacheDirectory)
	cachePath := filepath.Join(cacheDir, contentSHA+".zip")
	writeTestArchive(t, cachePath, content)

	audit, err := service.AuditArchiveStorage(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	var cacheItem *StorageAuditItem
	for i := range audit.Items {
		if audit.Items[i].Classification == classLegacyCacheRedundant {
			cacheItem = &audit.Items[i]
			break
		}
	}
	if cacheItem == nil {
		t.Fatal("no redundant cache item found")
	}

	result, err := service.ApplyStorageCleanup(context.Background(), audit.Fingerprint, []string{cacheItem.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Failures) != 0 {
		t.Errorf("unexpected failures: %v", result.Failures)
	}
	if result.RemovedCopies != 1 {
		t.Errorf("removedCopies = %d, want 1", result.RemovedCopies)
	}
	if _, statErr := os.Stat(cachePath); !os.IsNotExist(statErr) {
		t.Errorf("cache copy survived cleanup: %v", statErr)
	}
	if _, statErr := os.Stat(sourcePath); statErr != nil {
		t.Errorf("canonical source was deleted: %v", statErr)
	}
}

func TestCleanupPartialFailureReportsHonestly(t *testing.T) {
	service := newTestAppService(t)
	root := filepath.Join(service.config.DataDir, "library")

	content := []byte("partial-failure-test-content" + strings.Repeat("d", 72))
	contentSHA := sha256Hex(content)
	sourcePath := filepath.Join(root, "partial.zip")
	writeTestArchive(t, sourcePath, content)
	archives := []ScanArchive{
		modFamilyScanArchive(t, root, "partial.zip", "Partial Mod", "Author", "1.0", "", contentSHA, "partial-fp", int64(len(content)), testArchiveModified(6), 0),
	}
	writeTestArchive(t, sourcePath, content)
	applyLibraryArchives(t, service.store, root, archives)

	cacheDir := filepath.Join(service.config.ProfileDir, legacyArchiveCacheDirectory)
	cachePath := filepath.Join(cacheDir, contentSHA+".zip")
	writeTestArchive(t, cachePath, content)

	audit, err := service.AuditArchiveStorage(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	var validID string
	for _, item := range audit.Items {
		if item.Classification == classLegacyCacheRedundant {
			validID = item.ID
			break
		}
	}
	if validID == "" {
		t.Fatal("no redundant cache item found")
	}

	result, err := service.ApplyStorageCleanup(context.Background(), audit.Fingerprint, []string{"nonexistent-item-id", validID})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Failures) != 1 {
		t.Errorf("failures = %d, want 1; failures: %v", len(result.Failures), result.Failures)
	}
	if result.RemovedCopies != 1 {
		t.Errorf("removedCopies = %d, want 1", result.RemovedCopies)
	}
}

func TestCleanupMirrorProtectsUnownedFiles(t *testing.T) {
	service := newTestAppService(t)

	// Create a collection mirror directory with a file not in the ledger.
	mirrorRoot := filepath.Join(service.config.ExportDir, collectionFolderDirectory)
	mirrorDir := filepath.Join(mirrorRoot, "test-coll-id", "Test Collection")
	content := []byte("user-added-file-in-mirror" + strings.Repeat("u", 75))
	mirrorPath := filepath.Join(mirrorDir, "user-mod.zip")
	writeTestArchive(t, mirrorPath, content)

	audit, err := service.AuditArchiveStorage(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	var mirrorItem *StorageAuditItem
	for i := range audit.Items {
		if audit.Items[i].Classification == classCollectionMirror {
			mirrorItem = &audit.Items[i]
			break
		}
	}
	if mirrorItem == nil {
		t.Fatal("no collection mirror item found")
	}
	if mirrorItem.CleanupAllowed {
		t.Error("unowned mirror file must NOT be cleanup-allowed")
	}
	if !strings.Contains(mirrorItem.Reason, "not tracked") {
		t.Errorf("reason = %q, want mention of ownership ledger", mirrorItem.Reason)
	}
}

// ---------------------------------------------------------------------------
// Recovery tests
// ---------------------------------------------------------------------------

func TestRecoverCacheOnlyImportsToLibrary(t *testing.T) {
	service := newTestAppService(t)

	// Create the library root so importOneMod can use it.
	libraryDir := filepath.Join(service.config.DataDir, "library")
	if err := os.MkdirAll(libraryDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Ensure the scan root includes the library directory.
	service.config.ScanRoots = append(service.config.ScanRoots, libraryDir)
	service.config.LibraryDir = libraryDir

	// Write a real valid mod archive in the cache directory.
	cacheDir := filepath.Join(service.config.ProfileDir, legacyArchiveCacheDirectory)
	cachePath := filepath.Join(cacheDir, "recovery-test-orphan.zip")
	archiveBytes := writeValidModArchive(t, cachePath, "Recovery Test Mod")
	archiveSHA := sha256Hex(archiveBytes)

	// Rename to SHA-based name.
	shaPath := filepath.Join(cacheDir, archiveSHA+".zip")
	if err := os.Rename(cachePath, shaPath); err != nil {
		t.Fatal(err)
	}

	audit, err := service.AuditArchiveStorage(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	var cacheOnlyItem *StorageAuditItem
	for i := range audit.Items {
		if audit.Items[i].Classification == classLegacyCacheOnly {
			cacheOnlyItem = &audit.Items[i]
			break
		}
	}
	if cacheOnlyItem == nil {
		t.Fatal("no cache-only item found for recovery")
	}
	if !cacheOnlyItem.RecoveryAllowed {
		t.Fatal("cache-only item should be recovery-allowed")
	}

	result, err := service.RecoverStorageArchive(context.Background(), audit.Fingerprint, cacheOnlyItem.ID)
	if err != nil {
		t.Fatalf("recovery failed: %v", err)
	}
	if result.Recovered != 1 {
		t.Errorf("recovered = %d, want 1", result.Recovered)
	}

	// Verify the library now contains the recovered mod.
	items, err := service.store.ListLibrary(context.Background(), "", "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range items {
		if strings.Contains(item.DisplayName, "Recovery Test Mod") {
			found = true
			break
		}
	}
	if !found {
		t.Error("recovered mod not found in library after recovery")
	}
}

func TestRecoverRejectsStaleFingerprint(t *testing.T) {
	service := newTestAppService(t)

	_, err := service.RecoverStorageArchive(context.Background(), "stale-fp", "item-1")
	if err == nil {
		t.Fatal("expected error for stale fingerprint")
	}
	if !strings.Contains(err.Error(), "changed since") {
		t.Errorf("error = %q, want stale-review message", err.Error())
	}
}

// ---------------------------------------------------------------------------
// Allocation accounting tests
// ---------------------------------------------------------------------------

func TestAuditAllocationAccountingDoesNotDoubleCountHardlinks(t *testing.T) {
	items := []StorageAuditItem{
		{
			ID: "a", Path: "/a.zip", Classification: classCanonicalSource,
			LogicalBytes: 1000, AllocatedBytes: 1024, LinkCount: 2,
			Identity: ArchiveFileIdentity{VolumeID: "vol1", FileID: "file1", IdentityKnown: true, AllocationKnown: true, SizeBytes: 1000, AllocatedBytes: 1024, Links: 2},
		},
		{
			ID: "b", Path: "/b.zip", Classification: classLegacyCacheRedundant,
			LogicalBytes: 1000, AllocatedBytes: 1024, LinkCount: 2,
			Identity: ArchiveFileIdentity{VolumeID: "vol1", FileID: "file1", IdentityKnown: true, AllocationKnown: true, SizeBytes: 1000, AllocatedBytes: 1024, Links: 2},
		},
	}

	audit := buildStorageAudit(items, nil)

	if audit.ApparentBytes != 2000 {
		t.Errorf("apparentBytes = %d, want 2000", audit.ApparentBytes)
	}
	if audit.UniqueAllocatedBytes != 1024 {
		t.Errorf("uniqueAllocatedBytes = %d, want 1024 (one physical file)", audit.UniqueAllocatedBytes)
	}
	if audit.SharedBytes != 1024 {
		t.Errorf("sharedBytes = %d, want 1024", audit.SharedBytes)
	}
	// Hardlink with Links>1 should NOT count as reclaimable.
	if audit.RedundantBytes != 0 {
		t.Errorf("redundantBytes = %d, want 0 (hardlink removal doesn't free data)", audit.RedundantBytes)
	}
}

func TestAuditCountsIndependentCopiesSeparately(t *testing.T) {
	items := []StorageAuditItem{
		{
			ID: "src", Path: "/source.zip", Classification: classCanonicalSource,
			LogicalBytes: 5000, AllocatedBytes: 5120, LinkCount: 1,
			Identity: ArchiveFileIdentity{VolumeID: "vol1", FileID: "file1", IdentityKnown: true, AllocationKnown: true, SizeBytes: 5000, AllocatedBytes: 5120, Links: 1},
		},
		{
			ID: "copy", Path: "/cache/abc.zip", Classification: classLegacyCacheRedundant,
			LogicalBytes: 5000, AllocatedBytes: 5120, LinkCount: 1, ReclaimableBytes: 5120,
			Identity: ArchiveFileIdentity{VolumeID: "vol1", FileID: "file2", IdentityKnown: true, AllocationKnown: true, SizeBytes: 5000, AllocatedBytes: 5120, Links: 1},
		},
	}

	audit := buildStorageAudit(items, nil)

	if audit.UniqueAllocatedBytes != 10240 {
		t.Errorf("uniqueAllocatedBytes = %d, want 10240", audit.UniqueAllocatedBytes)
	}
	if audit.SharedBytes != 0 {
		t.Errorf("sharedBytes = %d, want 0", audit.SharedBytes)
	}
	if audit.RedundantBytes != 5120 {
		t.Errorf("redundantBytes = %d, want 5120", audit.RedundantBytes)
	}
}

func TestAuditRetainedBytesDeduplicatedByFileID(t *testing.T) {
	// Two paths pointing to the same retained file should count once.
	items := []StorageAuditItem{
		{
			ID: "retained-a", Path: "/cache/orphan.zip", Classification: classLegacyCacheOnly,
			LogicalBytes: 3000, AllocatedBytes: 4096, LinkCount: 2,
			Identity: ArchiveFileIdentity{VolumeID: "vol1", FileID: "orphan-id", IdentityKnown: true, AllocationKnown: true, SizeBytes: 3000, AllocatedBytes: 4096, Links: 2},
		},
		{
			ID: "retained-b", Path: "/mirror/orphan.zip", Classification: classLegacyCacheOnly,
			LogicalBytes: 3000, AllocatedBytes: 4096, LinkCount: 2,
			Identity: ArchiveFileIdentity{VolumeID: "vol1", FileID: "orphan-id", IdentityKnown: true, AllocationKnown: true, SizeBytes: 3000, AllocatedBytes: 4096, Links: 2},
		},
	}

	audit := buildStorageAudit(items, nil)

	if audit.RetainedBytes != 4096 {
		t.Errorf("retainedBytes = %d, want 4096 (counted once despite two names)", audit.RetainedBytes)
	}
}

// ---------------------------------------------------------------------------
// retireLegacyArchiveReferences tests
// ---------------------------------------------------------------------------

func TestRetireLegacyReferencesPreservesSoleCopy(t *testing.T) {
	service := newTestAppService(t)
	root := filepath.Join(service.config.DataDir, "library")

	content := []byte("sole-copy-retire-test" + strings.Repeat("e", 79))
	contentSHA := sha256Hex(content)

	sourcePath := filepath.Join(root, "sole-retire.zip")
	writeTestArchive(t, sourcePath, content)
	archives := []ScanArchive{
		modFamilyScanArchive(t, root, "sole-retire.zip", "Sole Retire", "Author", "1.0", "", contentSHA, "sole-fp", int64(len(content)), testArchiveModified(7), 0),
	}
	writeTestArchive(t, sourcePath, content)
	items := applyLibraryArchives(t, service.store, root, archives)
	if len(items) == 0 {
		t.Fatal("no items indexed")
	}

	os.Remove(sourcePath)

	cacheDir := filepath.Join(service.config.ProfileDir, legacyArchiveCacheDirectory)
	cachePath := filepath.Join(cacheDir, contentSHA+".zip")
	writeTestArchive(t, cachePath, content)

	service.modImportMu.Lock()
	err := service.retireLegacyArchiveReferences(context.Background(), []string{items[0].EntityID})
	service.modImportMu.Unlock()

	if err != nil {
		t.Fatalf("retireLegacyArchiveReferences error: %v", err)
	}
	if _, statErr := os.Stat(cachePath); statErr != nil {
		t.Errorf("sole surviving cache copy was deleted: %v", statErr)
	}
}

func TestRetireLegacyReferencesRemovesCopyWhenCanonicalExists(t *testing.T) {
	service := newTestAppService(t)
	root := filepath.Join(service.config.DataDir, "library")

	content := []byte("retire-with-canonical" + strings.Repeat("f", 79))
	contentSHA := sha256Hex(content)
	sourcePath := filepath.Join(root, "retire-canonical.zip")
	writeTestArchive(t, sourcePath, content)
	archives := []ScanArchive{
		modFamilyScanArchive(t, root, "retire-canonical.zip", "Retire Canon", "Author", "1.0", "", contentSHA, "retire-fp", int64(len(content)), testArchiveModified(8), 0),
	}
	writeTestArchive(t, sourcePath, content)
	items := applyLibraryArchives(t, service.store, root, archives)
	if len(items) == 0 {
		t.Fatal("no items indexed")
	}

	cacheDir := filepath.Join(service.config.ProfileDir, legacyArchiveCacheDirectory)
	cachePath := filepath.Join(cacheDir, contentSHA+".zip")
	writeTestArchive(t, cachePath, content)

	service.modImportMu.Lock()
	err := service.retireLegacyArchiveReferences(context.Background(), []string{items[0].EntityID})
	service.modImportMu.Unlock()

	if err != nil {
		t.Fatalf("retireLegacyArchiveReferences error: %v", err)
	}
	if _, statErr := os.Stat(sourcePath); statErr != nil {
		t.Errorf("canonical source was deleted: %v", statErr)
	}
	if _, statErr := os.Stat(cachePath); !os.IsNotExist(statErr) {
		t.Errorf("cache copy survived retirement: %v", statErr)
	}
}

func TestAuditManagedDeploymentUnownedRecoveryAllowed(t *testing.T) {
	service := newTestAppService(t)

	// Create a ZIP in the managed directory that is NOT in the ledger.
	managedDir := filepath.Join(service.config.ActiveModsDir, managedModDirectoryName)
	if err := os.MkdirAll(managedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte("unindexed-managed-zip" + strings.Repeat("g", 79))
	managedPath := filepath.Join(managedDir, "mystery-mod.zip")
	writeTestArchive(t, managedPath, content)

	audit, err := service.AuditArchiveStorage(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	var deployItem *StorageAuditItem
	for i := range audit.Items {
		if audit.Items[i].Classification == classManagedDeployment &&
			strings.HasSuffix(audit.Items[i].Path, "mystery-mod.zip") {
			deployItem = &audit.Items[i]
			break
		}
	}
	if deployItem == nil {
		t.Fatal("unowned managed ZIP not found in audit")
	}
	if deployItem.CleanupAllowed {
		t.Error("unowned managed ZIP must NOT be cleanup-allowed")
	}
	if !deployItem.RecoveryAllowed {
		t.Error("unowned managed ZIP should be recovery-allowed")
	}
	if !strings.Contains(deployItem.Reason, "recovery available") {
		t.Errorf("reason = %q, want mention of recovery", deployItem.Reason)
	}
}

func TestRecoverManagedZIPRegistersAsOwned(t *testing.T) {
	service := newTestAppService(t)

	// Create the library root.
	libraryDir := filepath.Join(service.config.DataDir, "library")
	if err := os.MkdirAll(libraryDir, 0o755); err != nil {
		t.Fatal(err)
	}
	service.config.ScanRoots = append(service.config.ScanRoots, libraryDir)
	service.config.LibraryDir = libraryDir

	// Place a real valid mod archive in the managed directory.
	managedDir := filepath.Join(service.config.ActiveModsDir, managedModDirectoryName)
	if err := os.MkdirAll(managedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	managedPath := filepath.Join(managedDir, "recover-managed.zip")
	writeValidModArchive(t, managedPath, "Managed Recovery Test")

	audit, err := service.AuditArchiveStorage(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	var deployItem *StorageAuditItem
	for i := range audit.Items {
		if audit.Items[i].Classification == classManagedDeployment && audit.Items[i].RecoveryAllowed {
			deployItem = &audit.Items[i]
			break
		}
	}
	if deployItem == nil {
		t.Fatal("recoverable managed ZIP not found in audit")
	}

	result, err := service.RecoverStorageArchive(context.Background(), audit.Fingerprint, deployItem.ID)
	if err != nil {
		t.Fatalf("recovery failed: %v", err)
	}
	if result.Recovered != 1 {
		t.Errorf("recovered = %d, want 1", result.Recovered)
	}

	// The managed ZIP should still exist (retained, not deleted).
	if _, statErr := os.Stat(managedPath); statErr != nil {
		t.Errorf("managed ZIP was deleted during recovery: %v", statErr)
	}

	// Verify the library now has the recovered mod.
	items, err := service.store.ListLibrary(context.Background(), "", "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range items {
		if strings.Contains(item.DisplayName, "Managed Recovery Test") {
			found = true
			break
		}
	}
	if !found {
		t.Error("recovered mod not found in library")
	}
}
