package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	modkit "github.com/SignedAdam/beamworlds-modkit"
)

// StorageProgress is defined in archive_deployment.go (Core owner).
// emitStorageProgress is defined in archive_deployment.go (Core owner).

// ---------------------------------------------------------------------------
// Models
// ---------------------------------------------------------------------------

// StorageAuditItem describes one audited file with its classification,
// identity, source relationship, and cleanup eligibility.
type StorageAuditItem struct {
	ID               string              `json:"id"`
	Path             string              `json:"path"`
	SourcePath       string              `json:"sourcePath"`
	EntityID         string              `json:"entityId"`
	ArtifactID       string              `json:"artifactId"`
	SHA256           string              `json:"sha256"`
	Purpose          string              `json:"purpose"`
	Classification   string              `json:"classification"`
	Reason           string              `json:"reason"`
	LogicalBytes     int64               `json:"logicalBytes"`
	AllocatedBytes   int64               `json:"allocatedBytes"`
	ReclaimableBytes int64               `json:"reclaimableBytes"`
	LinkCount        int                 `json:"linkCount"`
	Identity         ArchiveFileIdentity `json:"identity"`
	SourceIdentity   ArchiveFileIdentity `json:"sourceIdentity"`
	CleanupAllowed   bool                `json:"cleanupAllowed"`
	RecoveryAllowed  bool                `json:"recoveryAllowed"`
}

// StorageAudit is the read-only result of a full storage audit including
// physical identity deduplication, classification, and allocation accounting.
type StorageAudit struct {
	Fingerprint          string             `json:"fingerprint"`
	CheckedAt            string             `json:"checkedAt"`
	Items                []StorageAuditItem `json:"items"`
	ApparentBytes        int64              `json:"apparentBytes"`
	UniqueAllocatedBytes int64              `json:"uniqueAllocatedBytes"`
	SharedBytes          int64              `json:"sharedBytes"`
	RequiredCopyBytes    int64              `json:"requiredCopyBytes"`
	RedundantBytes       int64              `json:"redundantBytes"`
	RetainedBytes        int64              `json:"retainedBytes"`
	Warnings             []string           `json:"warnings"`
	ProtectedCategories []StorageCategorySummary `json:"protectedCategories"`
	AllocationEstimated bool `json:"allocationEstimated"`
}

// StorageCleanupResult reports the outcome of applying a storage cleanup or
// recovery operation including honest partial results.
type StorageCleanupResult struct {
	OperationID       string   `json:"operationId"`
	RemovedLinks      int      `json:"removedLinks"`
	RemovedCopies     int      `json:"removedCopies"`
	Recovered         int      `json:"recovered"`
	ReclaimedBytes    int64    `json:"reclaimedBytes"`
	ReclaimedEstimate bool     `json:"reclaimedEstimate"`
	Failures          []string `json:"failures"`
	RetainedPaths     []string `json:"retainedPaths"`
}

// storageCleanupJournalEntry is persisted per-item so cleanup/recovery results
// survive crashes and are available for audit. Stored in the deployment journal
// table with purpose "storage-cleanup" or "storage-recovery".
type storageCleanupJournalEntry struct {
	ItemID         string `json:"itemId"`
	Path           string `json:"path"`
	Classification string `json:"classification"`
	Action         string `json:"action"` // removed-link, removed-copy, recovered, failed, skipped
	Detail         string `json:"detail"`
	BytesClaimed   int64  `json:"bytesClaimed"`
}

// ---------------------------------------------------------------------------
// Classifications
// ---------------------------------------------------------------------------

const (
	classCanonicalSource      = "canonical-source"
	classLegacyCacheRedundant = "legacy-cache-redundant"
	classLegacyCacheOnly      = "legacy-cache-only"
	classCollectionMirror     = "collection-mirror"
	classManagedDeployment    = "managed-deployment"
	classUserExport           = "user-export"
	classUnknown              = "unknown"
)

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// auditSourceRecord is a snapshot of one indexed archive from the source catalog.
type auditSourceRecord struct {
	entityID    string
	artifactID  string
	path        string
	rootPath    string
	sha256      string
	sizeBytes   int64
	modifiedAt  string
	displayName string
	archivedAt  string
	linkID      string
	active      bool
}

// physicalFileKey uniquely identifies a physical file by volume+fileID.
type physicalFileKey struct {
	volumeID string
	fileID   string
}

// stableAuditItemID produces a deterministic ID from the normalized path and
// purpose, so re-audits of the same filesystem state produce the same IDs and
// fingerprints remain stable across Apply/Recover re-validations.
func stableAuditItemID(purpose, absPath string) string {
	h := sha256.Sum256([]byte(purpose + "\x00" + strings.ToLower(filepath.Clean(absPath))))
	return hex.EncodeToString(h[:16]) // 128-bit, collision-safe for <2^64 files
}

// ---------------------------------------------------------------------------
// AuditArchiveStorage — read-only, cancellable
// ---------------------------------------------------------------------------

// AuditArchiveStorage performs a non-destructive, cancellable read-only
// inventory of archive storage. It scans configured source roots, the legacy
// archive cache, collection mirror directories, and the managed deployment
// directory. Each file is classified and deduplicated by physical identity.
//
// All archive links (active AND inactive/archived) are included in the source
// catalog so retained versions are accounted for.
//
// Caller does NOT need to hold modImportMu — this is read-only.
func (service *AppService) AuditArchiveStorage(ctx context.Context) (audit StorageAudit, resultErr error) {
	operationID, idErr := modkit.NewID()
	if idErr != nil {
		return StorageAudit{}, idErr
	}
	progress := StorageProgress{OperationID: operationID, Phase: "scanning"}
	service.emitStorageProgress(progress)

	// Emit terminal progress on every exit path.
	defer func() {
		if resultErr != nil {
			progress.Phase = "failed"
			progress.Error = resultErr.Error()
			progress.Done = true
			service.emitStorageProgress(progress)
		}
	}()

	// 1. Snapshot the full source catalog (active AND inactive).
	sources, err := service.store.snapshotSourceCatalog(ctx)
	if err != nil {
		return StorageAudit{}, fmt.Errorf("read source catalog: %w", err)
	}
	sourceByPath := make(map[string]*auditSourceRecord, len(sources))
	sourceBySHA := make(map[string][]*auditSourceRecord)
	for i := range sources {
		rec := &sources[i]
		key := strings.ToLower(filepath.Clean(rec.path))
		if _, exists := sourceByPath[key]; !exists { sourceByPath[key] = rec }
		if rec.sha256 != "" {
			sourceBySHA[strings.ToLower(rec.sha256)] = append(sourceBySHA[strings.ToLower(rec.sha256)], rec)
		}
	}

	// 2. Load owned archive entries ledger for mirror classification.
	ownedEntries, err := service.store.listOwnedArchiveEntries(ctx)
	if err != nil { return StorageAudit{}, err }
	ownedByTarget := make(map[string]*OwnedArchiveEntry, len(ownedEntries))
	for i := range ownedEntries {
		e := &ownedEntries[i]
		target := strings.ToLower(filepath.Clean(filepath.Join(e.TargetRoot, e.RelativePath)))
		ownedByTarget[target] = e
	}

	var items []StorageAuditItem
	var warnings []string
	scanned := make(map[string]bool)
	// Retained indexed sources are authoritative even when their old scan root
	// is no longer configured. Inventory them directly rather than losing them
	// behind the scanner's generated-directory exclusions.
	for _, source := range sources {
		if err := ctx.Err(); err != nil { return StorageAudit{}, err }
		path := cleanOptionalPath(source.path)
		if path == "" || scanned[archivePathKey(path)] { continue }
		generated := false
		for _, root := range []string{
			filepath.Join(service.config.ProfileDir,legacyArchiveCacheDirectory),
			filepath.Join(service.config.ExportDir,collectionFolderDirectory),
			filepath.Join(service.config.ActiveModsDir,managedModDirectoryName),
		} { if pathWithin(path,root) { generated=true; break } }
		if generated { continue }
		// Folder mods are directories; account using stored size from the manifest.
		if info, statErr := os.Lstat(path); statErr == nil && info.IsDir() {
			scanned[archivePathKey(path)]=true
			items=append(items,StorageAuditItem{
				ID:stableAuditItemID("canonical",path),Path:path,Purpose:"canonical",
				Classification:classCanonicalSource,Reason:"authoritative indexed folder source",
				EntityID:source.entityID,ArtifactID:source.artifactID,SHA256:source.sha256,
				LogicalBytes:source.sizeBytes,AllocatedBytes:source.sizeBytes,
			})
			continue
		}
		identity, exists, err := archiveIdentityIfPresent(path)
		if err != nil { warnings=append(warnings,fmt.Sprintf("inspect indexed archive %s: %v",path,err)); continue }
		if !exists { continue }
		scanned[archivePathKey(path)]=true
		items=append(items,StorageAuditItem{
			ID:stableAuditItemID("canonical",path),Path:path,Purpose:"canonical",
			Classification:classCanonicalSource,Reason:"authoritative indexed source",
			EntityID:source.entityID,ArtifactID:source.artifactID,SHA256:source.sha256,
			LogicalBytes:identity.SizeBytes,AllocatedBytes:identity.AllocatedBytes,
			LinkCount:identity.Links,Identity:identity,
		})
	}

	// 3. Scan configured source roots.
	scanRoots := effectiveScanRoots(service.config)
	for _, root := range scanRoots {
		if err := ctx.Err(); err != nil {
			return StorageAudit{}, err
		}
		rootItems, rootWarnings, scanErr := service.scanCanonicalSources(ctx, root, sourceByPath, scanned, &progress)
		if scanErr != nil {
			// Propagate cancellation rather than burying it in warnings.
			if errors.Is(scanErr, context.Canceled) || errors.Is(scanErr, context.DeadlineExceeded) {
				return StorageAudit{}, scanErr
			}
			warnings = append(warnings, fmt.Sprintf("scan root %s: %v", root, scanErr))
			continue
		}
		items = append(items, rootItems...)
		warnings = append(warnings, rootWarnings...)
	}

	// 4. Scan legacy archive cache.
	if service.config.ProfileDir != "" {
		cacheDir := filepath.Join(service.config.ProfileDir, legacyArchiveCacheDirectory)
		cacheItems, cacheWarnings, scanErr := service.scanLegacyCache(ctx, cacheDir, sourceByPath, sourceBySHA, scanned, &progress)
		if scanErr != nil {
			if errors.Is(scanErr, context.Canceled) || errors.Is(scanErr, context.DeadlineExceeded) {
				return StorageAudit{}, scanErr
			}
			warnings = append(warnings, fmt.Sprintf("legacy cache: %v", scanErr))
		} else {
			items = append(items, cacheItems...)
			warnings = append(warnings, cacheWarnings...)
		}
	}

	// 5. Scan collection mirror directories.
	if service.config.ExportDir != "" {
		mirrorRoot := filepath.Join(service.config.ExportDir, collectionFolderDirectory)
		mirrorItems, mirrorWarnings, scanErr := service.scanCollectionMirrors(ctx, mirrorRoot, ownedByTarget, sourceByPath, scanned, &progress)
		if scanErr != nil {
			if errors.Is(scanErr, context.Canceled) || errors.Is(scanErr, context.DeadlineExceeded) {
				return StorageAudit{}, scanErr
			}
			warnings = append(warnings, fmt.Sprintf("collection mirrors: %v", scanErr))
		} else {
			items = append(items, mirrorItems...)
			warnings = append(warnings, mirrorWarnings...)
		}
	}

	// 6. Scan deployment work directories (staging/previous retention).
	workDirRoots := service.deploymentWorkDirRoots()
	pendingJournals, err := service.store.listPendingDeploymentJournals(ctx)
	if err != nil { return StorageAudit{}, err }
	recoveryItems,err:=service.auditRecoveryPreparations(ctx,pendingJournals,scanned)
	if err!=nil{return StorageAudit{},err}
	items=append(items,recoveryItems...)
	for _, workRoot := range workDirRoots {
		if err := ctx.Err(); err != nil {
			return StorageAudit{}, err
		}
		workItems, workWarnings, scanErr := service.scanDeploymentWorkDir(ctx, workRoot, pendingJournals, scanned, &progress)
		if scanErr != nil {
			if errors.Is(scanErr, context.Canceled) || errors.Is(scanErr, context.DeadlineExceeded) {
				return StorageAudit{}, scanErr
			}
			warnings = append(warnings, fmt.Sprintf("deployment work dir %s: %v", workRoot, scanErr))
		} else {
			items = append(items, workItems...)
			warnings = append(warnings, workWarnings...)
		}
	}

	// 7. Scan managed deployment directory (profile mods + legacy managed dir).
	if playRoot, playErr := playUserPath(service.config); playErr == nil {
		profileMods := playProfileModsDir(playRoot)
		deployItems, deployWarnings, scanErr := service.scanManagedDeployment(ctx, profileMods, ownedByTarget, sourceByPath, scanned, &progress)
		if scanErr != nil {
			if errors.Is(scanErr, context.Canceled) || errors.Is(scanErr, context.DeadlineExceeded) {
				return StorageAudit{}, scanErr
			}
			warnings = append(warnings, fmt.Sprintf("profile mods: %v", scanErr))
		} else {
			items = append(items, deployItems...)
			warnings = append(warnings, deployWarnings...)
		}
	}
	if service.config.ActiveModsDir != "" {
		managedRoot := filepath.Join(service.config.ActiveModsDir, managedModDirectoryName)
		deployItems, deployWarnings, scanErr := service.scanManagedDeployment(ctx, managedRoot, ownedByTarget, sourceByPath, scanned, &progress)
		if scanErr != nil {
			if errors.Is(scanErr, context.Canceled) || errors.Is(scanErr, context.DeadlineExceeded) {
				return StorageAudit{}, scanErr
			}
			warnings = append(warnings, fmt.Sprintf("managed deployment: %v", scanErr))
		} else {
			items = append(items, deployItems...)
			warnings = append(warnings, deployWarnings...)
		}
	}
	exportItems,exportWarnings,err:=service.auditExplicitExports(ctx,scanned)
	if err!=nil{return StorageAudit{},err}
	items=append(items,exportItems...);warnings=append(warnings,exportWarnings...)
	if err:=service.verifyStorageCleanupCandidates(ctx,items,&progress);err!=nil{return StorageAudit{},err}

	// 8. Build final audit with physical deduplication.
	audit = buildStorageAudit(items, warnings)
	if err:=service.accountProtectedStorage(ctx,&audit,&progress);err!=nil{return StorageAudit{},err}

	progress.Phase = "complete"
	progress.Done = true
	progress.Completed = len(items)
	progress.Total = len(items)
	service.emitStorageProgress(progress)

	return audit, nil
}

// ---------------------------------------------------------------------------
// ApplyStorageCleanup — fingerprint-validated, journaled, partial-safe
// ---------------------------------------------------------------------------

// Journal purpose constants for storage operations. Main's startup recovery
// must treat these as non-play operations — they never represent a play
// selection swap and must not trigger deployment rollback.
const (
	storageCleanupPurpose  = "storage-cleanup"
	storageRecoveryPurpose = "storage-recovery"
)

// ApplyStorageCleanup removes verified redundant archives. It validates the
// audit fingerprint, revalidates per-file identities, verifies canonical
// sources (SHA256 for independent copies), guards against a running game,
// persists per-item intent before each deletion and result afterward, and
// reports honest partial results. Finalization writes the journal outcome
// regardless of cancellation.
//
// Caller must NOT hold modImportMu or store.writeMu.
func (service *AppService) ApplyStorageCleanup(ctx context.Context, fingerprint string, itemIDs []string) (result StorageCleanupResult, resultErr error) {
	operationID, err := modkit.NewID()
	if err != nil {
		return StorageCleanupResult{}, err
	}
	result = StorageCleanupResult{
		OperationID:   operationID,
		Failures:      []string{},
		RetainedPaths: []string{},
		ReclaimedEstimate: true,
	}
	var journalID string
	var journalItems []storageCleanupJournalEntry
	locked:=false
	progress := StorageProgress{OperationID: operationID, Phase: "validating", Total: len(itemIDs)}
	service.emitStorageProgress(progress)
	defer func() { service.finalizeStorageOperation(ctx,journalID,journalItems,&result,&progress,&resultErr); if locked { service.modImportMu.Unlock() } }()
	if strings.TrimSpace(fingerprint) == "" || len(itemIDs) == 0 {
		return result, errors.New("fingerprint and item IDs are required")
	}

	service.modImportMu.Lock()
	locked=true
	if err:=ctx.Err();err!=nil{return result,err}

	if err := service.requireGameStopped(); err != nil {
		return result, err
	}


	journalID, err = modkit.NewID()
	if err != nil {
		return result, err
	}
	if err := service.store.writeDeploymentJournal(ctx, deploymentJournalEntry{
		ID: journalID, OperationID: operationID, State: journalStatePlanned,
		Purpose: storageCleanupPurpose, OwnerID: operationID, StartedAt: nowUTC(),
	}); err != nil {
		return result, fmt.Errorf("persist cleanup journal: %w", err)
	}


	freshAudit, err := service.AuditArchiveStorage(ctx)
	if err != nil {
		return result, fmt.Errorf("revalidate storage audit: %w", err)
	}
	if freshAudit.Fingerprint != fingerprint {
		return result, errors.New("the storage audit changed since this review was generated; run a fresh audit")
	}

	freshByID := make(map[string]*StorageAuditItem, len(freshAudit.Items))
	for i := range freshAudit.Items {
		freshByID[freshAudit.Items[i].ID] = &freshAudit.Items[i]
	}

	if err := service.store.updateDeploymentJournalState(ctx, journalID, journalStateActivating); err != nil {
		return result, fmt.Errorf("update cleanup journal state: %w", err)
	}

	progress.Phase = "cleaning"
	service.emitStorageProgress(progress)

	for itemIndex, itemID := range itemIDs {
		if err := ctx.Err(); err != nil {
			for _, remainingID := range itemIDs[itemIndex:] { if item:=freshByID[remainingID]; item!=nil { result.RetainedPaths=append(result.RetainedPaths,item.Path) } }
			result.Failures = append(result.Failures, fmt.Sprintf("cancelled: %v", err))
			journalItems = append(journalItems, storageCleanupJournalEntry{
				ItemID: itemID, Action: "failed", Detail: "cancelled",
			})
			break
		}
		if gsErr := service.requireGameStopped(); gsErr != nil {
			for _, remainingID := range itemIDs[itemIndex:] { if item:=freshByID[remainingID]; item!=nil { result.RetainedPaths=append(result.RetainedPaths,item.Path) } }
			result.Failures = append(result.Failures, fmt.Sprintf("game started during cleanup: %v", gsErr))
			journalItems = append(journalItems, storageCleanupJournalEntry{
				ItemID: itemID, Action: "failed", Detail: gsErr.Error(),
			})
			break
		}

		freshItem, exists := freshByID[itemID]
		if !exists {
			result.Failures = append(result.Failures, fmt.Sprintf("%s: no longer present in audit", itemID))
			journalItems = append(journalItems, storageCleanupJournalEntry{
				ItemID: itemID, Action: "skipped", Detail: "not in audit",
			})
			continue
		}
		if !freshItem.CleanupAllowed {
			result.Failures = append(result.Failures, fmt.Sprintf("%s (%s): cleanup not allowed — %s",
				freshItem.Path, freshItem.Classification, freshItem.Reason))
			result.RetainedPaths = append(result.RetainedPaths, freshItem.Path)
			journalItems = append(journalItems, storageCleanupJournalEntry{
				ItemID: itemID, Path: freshItem.Path, Classification: freshItem.Classification,
				Action: "skipped", Detail: freshItem.Reason,
			})
			continue
		}

		// Persist intent BEFORE mutation.
		intentEntry := storageCleanupJournalEntry{
			ItemID: itemID, Path: freshItem.Path, Classification: freshItem.Classification,
			Action: "pending",
		}
		intentJSON, _ := json.Marshal(append(journalItems, intentEntry))
		if err := service.store.completeDeploymentJournal(ctx,journalID,journalStateActivating,string(intentJSON)); err != nil {
			return result, fmt.Errorf("persist cleanup intent: %w",err)
		}

		cleanErr := service.cleanupOneItem(ctx, freshItem, &result)

		jEntry := storageCleanupJournalEntry{
			ItemID: itemID, Path: freshItem.Path, Classification: freshItem.Classification,
		}
		if cleanErr != nil {
			result.Failures = append(result.Failures, fmt.Sprintf("%s: %v", freshItem.Path, cleanErr))
			if _, statErr:=os.Lstat(freshItem.Path); !errors.Is(statErr,os.ErrNotExist) { result.RetainedPaths=append(result.RetainedPaths,freshItem.Path) }
			jEntry.Action = "failed"
			jEntry.Detail = cleanErr.Error()
		} else if freshItem.ReclaimableBytes > 0 && freshItem.Identity.Links <= 1 {
			jEntry.Action = "removed-copy"
			jEntry.BytesClaimed = freshItem.ReclaimableBytes
		} else {
			jEntry.Action = "removed-link"
		}
		journalItems = append(journalItems, jEntry)
		completedJSON,_:=json.Marshal(journalItems)
		if err:=service.store.completeDeploymentJournal(ctx,journalID,journalStateActivating,string(completedJSON));err!=nil { return result,fmt.Errorf("persist cleanup progress: %w",err) }

		progress.Completed++
		progress.Current = filepath.Base(freshItem.Path)
		service.emitStorageProgress(progress)
	}

	return result, nil
}

// ---------------------------------------------------------------------------
// RecoverStorageArchive — import + adoption, journaled
// ---------------------------------------------------------------------------

// RecoverStorageArchive imports a cache-only or unindexed managed archive into
// the canonical library with full verification and indexing.
//
// Same-volume: creates a hardlink in the library destination, then inspects
// and indexes the linked file directly (modkit.Inspect + modkit.FullSHA256 +

// ---------------------------------------------------------------------------
// Deployment work directory scanning
// ---------------------------------------------------------------------------

// deploymentWorkDirRoots returns the deployment work directories that may
// contain staging/previous retained archives. These are the same roots that
// Core's applyArchiveDeployment uses for journaled staging.
func (service *AppService) deploymentWorkDirRoots() []string {
	var roots []string
	// Profile-based Play deployment work directory.
	if playRoot, err := playUserPath(service.config); err == nil {
		roots = append(roots, playProfileDeploymentDir(playRoot))
	}
	// Legacy managed-dir work root (for pre-migration journals).
	if service.config.ActiveModsDir != "" {
		managedRoot := filepath.Join(service.config.ActiveModsDir, managedModDirectoryName)
		roots = append(roots, deploymentWorkDir(managedRoot))
	}
	if service.config.ExportDir != "" {
		collRoot := filepath.Join(service.config.ExportDir, ".beamworlds-deployment")
		roots = append(roots, collRoot)
	}
	return roots
}

// scanDeploymentWorkDir inventories a deployment work directory for .staging
// files. Files owned by pending journal entries (StagingDir or PreviousDir)
// are classified and may be recoverable. Unknown .staging files are protected.
func (service *AppService) scanDeploymentWorkDir(ctx context.Context, workRoot string, journals []deploymentJournalEntry, scanned map[string]bool, progress *StorageProgress) ([]StorageAuditItem, []string, error) {
	var items []StorageAuditItem
	var warnings []string

	if _, err := os.Stat(workRoot); err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}

	// Build lookup of journal-owned directories.
	journalStagingDirs := make(map[string]*deploymentJournalEntry)
	journalPreviousDirs := make(map[string]*deploymentJournalEntry)
	for i := range journals {
		j := &journals[i]
		if j.StagingDir != "" {
			journalStagingDirs[strings.ToLower(filepath.Clean(j.StagingDir))] = j
		}
		if j.PreviousDir != "" {
			journalPreviousDirs[strings.ToLower(filepath.Clean(j.PreviousDir))] = j
		}
	}

	// Walk the work root (shallow: staging dirs are direct children).
	entries, err := os.ReadDir(workRoot)
	if err != nil {
		return nil, nil, err
	}

	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return items, warnings, err
		}
		if !entry.IsDir() {
			continue
		}
		dirPath := filepath.Join(workRoot, entry.Name())
		lowDir := strings.ToLower(filepath.Clean(dirPath))

		// Determine ownership.
		var ownerJournal *deploymentJournalEntry
		ownerRole := "" // "staging" or "previous"
		if j, ok := journalStagingDirs[lowDir]; ok {
			ownerJournal = j
			ownerRole = "staging"
		} else if j, ok := journalPreviousDirs[lowDir]; ok {
			ownerJournal = j
			ownerRole = "previous"
		}

		// Scan files inside the directory.
		files, readErr := os.ReadDir(dirPath)
		if readErr != nil {
			warnings = append(warnings, fmt.Sprintf("read deployment work dir %s: %v", dirPath, readErr))
			continue
		}

		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".staging") {
				continue
			}
			filePath := filepath.Join(dirPath, f.Name())
			absPath, _ := filepath.Abs(filePath)
			lowPath := strings.ToLower(filepath.Clean(absPath))
			if scanned[lowPath] {
				continue
			}
			scanned[lowPath] = true

			identity, inspectErr := inspectArchiveFile(absPath)
			if inspectErr != nil {
				warnings = append(warnings, fmt.Sprintf("inspect staging %s: %v", absPath, inspectErr))
				continue
			}

			item := StorageAuditItem{
				ID:             stableAuditItemID("staging", absPath),
				Path:           absPath,
				LogicalBytes:   identity.SizeBytes,
				AllocatedBytes: identity.AllocatedBytes,
				LinkCount:      identity.Links,
				Identity:       identity,
			}

			if ownerJournal != nil {
				item.Purpose = fmt.Sprintf("journal-%s-%s", ownerJournal.Purpose, ownerRole)

				// Match the .staging file to a journal entry by ID prefix.
				entryID := strings.TrimSuffix(f.Name(), ".staging")
				var matchedEntry *OwnedArchiveEntry
				if ownerRole == "staging" {
					for j := range ownerJournal.PreparedEntries {
						if ownerJournal.PreparedEntries[j].ID == entryID {
							matchedEntry = &ownerJournal.PreparedEntries[j]
							break
						}
					}
				} else {
					for j := range ownerJournal.PriorOwned {
						if ownerJournal.PriorOwned[j].ID == entryID {
							matchedEntry = &ownerJournal.PriorOwned[j]
							break
						}
					}
				}

				if matchedEntry != nil {
					item.EntityID = matchedEntry.EntityID
					item.ArtifactID = matchedEntry.ArtifactID
					item.SHA256 = matchedEntry.SHA256
					item.SourcePath = matchedEntry.SourcePath
					item.Classification = classManagedDeployment
					if ownerRole == "previous" {
						item.Reason = "retained previous deployment archive (journal-owned)"
						item.RecoveryAllowed = true
					} else {
						item.Reason = "staged deployment archive (journal-owned)"
						item.RecoveryAllowed = false
					}
					item.CleanupAllowed = false
				} else {
					// .staging file in a journal-owned directory but not matching
					// any entry — protect it.
					item.Classification = classUnknown
					item.Reason = "unmatched .staging file in journal-owned directory"
					item.CleanupAllowed = false
					item.RecoveryAllowed = false
				}
			} else {
				// Unknown .staging file not associated with any pending journal.
				item.Purpose = "unknown-staging"
				item.Classification = classUnknown
				item.Reason = "unknown .staging file in deployment work directory — protected"
				item.CleanupAllowed = false
				item.RecoveryAllowed = false
			}

			items = append(items, item)
			progress.Completed++
			progress.Current = f.Name()
			service.emitStorageProgress(*progress)
		}
	}
	return items, warnings, nil
}


// ---------------------------------------------------------------------------
// retireLegacyArchiveReferences — for Main's removal/replacement integration
// ---------------------------------------------------------------------------

// retireLegacyArchiveReferences removes app-owned generated references (legacy
// cache copies, collection mirror links, owned ledger entries) that belong to
// the given entity IDs. Called by Main's removal/replacement integration after
// approved removals so generated refs don't outlive their sources.
//
// Safety: preserves sole surviving data (cache-only with no canonical source).
// Validates fresh canonical identity and ownership before removing any path.
//
// Lock ownership: caller MUST hold modImportMu. This method does NOT acquire
// store.writeMu and does NOT hold a SQLite write transaction over filesystem
// operations.
func (service *AppService) retireLegacyArchiveReferences(ctx context.Context, entityIDs []string) error {
	if service.config.ProfileDir=="" || len(entityIDs)==0 { return nil }
	sources,err:=service.store.snapshotSourceCatalogForEntities(ctx,entityIDs)
	if err!=nil{return err}
	seen:=map[string]bool{}
	for _,source:=range sources{
		hash:=strings.ToLower(source.sha256)
		if len(hash)!=64{continue}
		if _,err:=hex.DecodeString(hash);err!=nil{continue}
		path:=filepath.Join(service.config.ProfileDir,legacyArchiveCacheDirectory,hash+".zip")
		if seen[path]{continue}
		current,exists,err:=archiveIdentityIfPresent(path);if err!=nil{return err};if !exists{continue}
		canonical,available,err:=archiveIdentityIfPresent(source.path)
		// A sole surviving copy is preserved for Review storage; it must not
		// block the removal the user asked for.
		if err!=nil || !available || !canonical.Regular{continue}
		seen[path]=true
		item:=StorageAuditItem{ID:stableAuditItemID("cache",path),Path:path,SourcePath:source.path,EntityID:source.entityID,ArtifactID:source.artifactID,SHA256:hash,Identity:current,SourceIdentity:canonical,Classification:classLegacyCacheRedundant,CleanupAllowed:true}
		id,err:=modkit.NewID();if err!=nil{return err}
		intent:=storageCleanupJournalEntry{ItemID:item.ID,Path:path,Classification:item.Classification,Action:"pending"}
		payload,err:=json.Marshal([]storageCleanupJournalEntry{intent});if err!=nil{return err}
		journal:=deploymentJournalEntry{ID:id,OperationID:id,State:journalStateActivating,Purpose:storageCleanupPurpose,OwnerID:id,StartedAt:nowUTC(),ErrorMessage:string(payload)}
		if err:=service.store.writeDeploymentJournal(ctx,journal);err!=nil{return err}
		result:=StorageCleanupResult{OperationID:id,ReclaimedEstimate:true}
		cleanupErr:=service.cleanupRedundantCache(ctx,&item,current,&result)
		state:=journalStateDone;intent.Action="removed"
		if cleanupErr!=nil{state=journalStateFailed;intent.Action="retained";intent.Detail=cleanupErr.Error()}
		payload,err=json.Marshal([]storageCleanupJournalEntry{intent});if err!=nil{return err}
		finalCtx,cancel:=context.WithTimeout(context.WithoutCancel(ctx),10*time.Second)
		saveErr:=service.store.completeDeploymentJournal(finalCtx,id,state,string(payload));cancel()
		// Cleanup failures are recorded as retained in the journal and remain
		// visible in Review storage; only a lost journal write is fatal.
		if saveErr!=nil{return saveErr}
	}
	// Collection mirrors are retired through their ownership ledger, never by
	// sweeping files that merely share a canonical file ID.
	return nil
}

// ---------------------------------------------------------------------------
// Scanning helpers
// ---------------------------------------------------------------------------

func (service *AppService) scanCanonicalSources(ctx context.Context, root string, sourceByPath map[string]*auditSourceRecord, scanned map[string]bool, progress *StorageProgress) ([]StorageAuditItem, []string, error) {
	var items []StorageAuditItem
	var warnings []string

	root = filepath.Clean(root)
	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, []string{fmt.Sprintf("scan root unavailable: %s: %v", root, err)}, nil
	}

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			warnings = append(warnings, fmt.Sprintf("walk %s: %v", path, walkErr))
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			dirName := d.Name()
			if dirName == legacyArchiveCacheDirectory || dirName == managedModDirectoryName ||
				dirName == collectionFolderDirectory ||
				strings.HasPrefix(dirName, ".beamworlds-managed-next-") ||
				dirName == ".beamworlds-managed-previous" {
				return filepath.SkipDir
			}
			// Skip Studio data directories that may be nested under a scan root.
			if service.config.DataDir != "" && pathWithin(path, service.config.DataDir) {
				return filepath.SkipDir
			}
			return nil
		}
		if !isModArchive(d.Name()) {
			return nil
		}
		absPath, err := filepath.Abs(path)
		if err != nil {
			absPath = path
		}
		lowPath := strings.ToLower(filepath.Clean(absPath))
		if scanned[lowPath] {
			return nil
		}
		scanned[lowPath] = true

		identity, inspectErr := inspectArchiveFile(absPath)
		if inspectErr != nil {
			warnings = append(warnings, fmt.Sprintf("inspect %s: %v", absPath, inspectErr))
			return nil
		}
		if !identity.Regular {
			warnings = append(warnings, fmt.Sprintf("skipped non-regular file: %s", absPath))
			return nil
		}

		item := StorageAuditItem{
			Path:           absPath,
			LogicalBytes:   identity.SizeBytes,
			AllocatedBytes: identity.AllocatedBytes,
			LinkCount:      identity.Links,
			Identity:       identity,
		}

		rec, found := sourceByPath[lowPath]
		if found {
			item.ID = stableAuditItemID("canonical", absPath)
			item.EntityID = rec.entityID
			item.ArtifactID = rec.artifactID
			item.SHA256 = rec.sha256
			item.Purpose = "canonical"
			item.Classification = classCanonicalSource
			item.Reason = "authoritative indexed source"
			item.CleanupAllowed = false
			item.RecoveryAllowed = false
		} else {
			item.ID = stableAuditItemID("unknown", absPath)
			item.Classification = classUnknown
			item.Purpose = "unknown"
			item.Reason = "archive in scan root but not in the current index"
			item.CleanupAllowed = false
			item.RecoveryAllowed = false
		}

		items = append(items, item)
		progress.Completed++
		progress.Current = d.Name()
		service.emitStorageProgress(*progress)
		return nil
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		return items, warnings, err
	}
	return items, warnings, nil
}

func (service *AppService) scanLegacyCache(ctx context.Context, cacheDir string, sourceByPath map[string]*auditSourceRecord, sourceBySHA map[string][]*auditSourceRecord, scanned map[string]bool, progress *StorageProgress) ([]StorageAuditItem, []string, error) {
	var items []StorageAuditItem
	var warnings []string

	info, err := os.Stat(cacheDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	if !info.IsDir() {
		return nil, []string{fmt.Sprintf("legacy cache is not a directory: %s", cacheDir)}, nil
	}

	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		return nil, nil, err
	}

	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return items, warnings, err
		}
		if entry.IsDir() || !isModArchive(entry.Name()) {
			continue
		}
		cachePath := filepath.Join(cacheDir, entry.Name())
		absPath, _ := filepath.Abs(cachePath)
		lowPath := strings.ToLower(filepath.Clean(absPath))
		if scanned[lowPath] {
			continue
		}
		scanned[lowPath] = true

		identity, inspectErr := inspectArchiveFile(absPath)
		if inspectErr != nil {
			warnings = append(warnings, fmt.Sprintf("inspect cache %s: %v", absPath, inspectErr))
			continue
		}
		if !identity.Regular {
			warnings = append(warnings, fmt.Sprintf("skipped non-regular cache entry: %s", absPath))
			continue
		}

		item := StorageAuditItem{
			Path:           absPath,
			LogicalBytes:   identity.SizeBytes,
			AllocatedBytes: identity.AllocatedBytes,
			LinkCount:      identity.Links,
			Identity:       identity,
			Purpose:        "legacy-cache",
		}

		baseName := strings.TrimSuffix(strings.ToLower(entry.Name()), ".zip")

		// Check if this cache entry is a hardlink to a canonical source.
		isHardlinkToSource := false
		if identity.IdentityKnown {
			for _, rec := range sourceBySHA[baseName] {
				srcPath := cleanOptionalPath(rec.path)
				if srcPath == "" {
					continue
				}
				srcIdentity, srcErr := inspectArchiveFile(srcPath)
				if srcErr != nil {
					continue
				}
				if srcIdentity.IdentityKnown &&
					srcIdentity.VolumeID == identity.VolumeID &&
					srcIdentity.FileID == identity.FileID {
					item.SourcePath = srcPath
					item.EntityID = rec.entityID
					item.ArtifactID = rec.artifactID
					item.SHA256 = rec.sha256
					item.SourceIdentity = srcIdentity
					isHardlinkToSource = true
					break
				}
			}
		}

		if isHardlinkToSource {
			item.ID = stableAuditItemID("cache-link", absPath)
			item.Classification = classLegacyCacheRedundant
			item.Reason = "hardlink to canonical source (removing unlinks this name only)"
			item.CleanupAllowed = true
			item.RecoveryAllowed = false
			item.ReclaimableBytes = 0
		} else if matchRecs := sourceBySHA[baseName]; len(matchRecs) > 0 {
			// Potential independent copy — verify source actually exists on disk
			// and is readable before marking as redundant/cleanable.
			rec := matchRecs[0]
			srcPath := cleanOptionalPath(rec.path)
			sourceExists := false
			if srcPath != "" {
				srcIdentity, srcErr := inspectArchiveFile(srcPath)
				if srcErr == nil && srcIdentity.Regular {
					sourceExists = true
					item.SourceIdentity = srcIdentity
				}
			}

			item.SourcePath = srcPath
			item.EntityID = rec.entityID
			item.ArtifactID = rec.artifactID
			item.SHA256 = rec.sha256

			if sourceExists {
				item.ID = stableAuditItemID("cache-copy", absPath)
				item.Classification = classLegacyCacheRedundant
				item.Reason = "independent copy of indexed source (SHA256 verification required before removal)"
				item.CleanupAllowed = true
				item.RecoveryAllowed = false
				// ReclaimableBytes only if this is the sole link to this data.
				if identity.Links <= 1 {
					item.ReclaimableBytes = identity.AllocatedBytes
					if !identity.AllocationKnown {
						item.ReclaimableBytes = identity.SizeBytes
					}
				}
			} else {
				// Source missing — this cache copy may be the sole survivor.
				// Classify as retained/recoverable, NOT cleanable.
				item.ID = stableAuditItemID("cache-retained", absPath)
				item.Classification = classLegacyCacheOnly
				item.Reason = "source archive missing; cache copy retained as sole surviving data"
				item.CleanupAllowed = false
				item.RecoveryAllowed = true
			}
		} else {
			// No matching indexed source at all.
			item.ID = stableAuditItemID("cache-only", absPath)
			item.Classification = classLegacyCacheOnly
			item.Reason = "legacy cache archive with no matching indexed source — preserved by default"
			item.CleanupAllowed = false
			item.RecoveryAllowed = true
		}

		items = append(items, item)
		progress.Completed++
		progress.Current = entry.Name()
		service.emitStorageProgress(*progress)
	}
	return items, warnings, nil
}

// scanCollectionMirrors reads collection folder directories. Owned entries
// are checked for staleness: if the owner collection is deleted, the entity
// is no longer in the collection's resolved selection, or the source
// identity/artifact has changed, the entry is stale. Stale entries with
// verified canonical source are CleanupAllowed. Unknown files and truly
// current mirrors are protected.
func (service *AppService) scanCollectionMirrors(ctx context.Context, mirrorRoot string, ownedByTarget map[string]*OwnedArchiveEntry, sourceByPath map[string]*auditSourceRecord, scanned map[string]bool, progress *StorageProgress) ([]StorageAuditItem, []string, error) {
	var items []StorageAuditItem
	var warnings []string

	if _, err := os.Stat(mirrorRoot); err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}

	// Cache resolved selections per collection to avoid redundant queries.
	type collectionLiveness struct {
		exists   bool
		entities map[string]bool
	}
	collectionCache := make(map[string]*collectionLiveness)

	checkCollectionLiveness := func(collectionID string) *collectionLiveness {
		if cached, ok := collectionCache[collectionID]; ok {
			return cached
		}
		live := &collectionLiveness{entities: make(map[string]bool)}
		_, err := service.store.CollectionDetail(ctx, collectionID)
		if err != nil {
			collectionCache[collectionID] = live
			return live
		}
		live.exists = true
		selection, selErr := service.store.ResolvePlaySelection(ctx, []string{collectionID}, nil)
		if selErr == nil {
			for _, mod := range selection.Mods {
				live.entities[mod.EntityID] = true
			}
		}
		collectionCache[collectionID] = live
		return live
	}

	collDirs, err := os.ReadDir(mirrorRoot)
	if err != nil {
		return nil, nil, err
	}

	for _, collDir := range collDirs {
		if !collDir.IsDir() {
			continue
		}
		collPath := filepath.Join(mirrorRoot, collDir.Name())
		subDirs, subErr := os.ReadDir(collPath)
		if subErr != nil {
			warnings = append(warnings, fmt.Sprintf("read collection mirror %s: %v", collPath, subErr))
			continue
		}
		for _, sub := range subDirs {
			if !sub.IsDir() {
				continue
			}
			searchDir := filepath.Join(collPath, sub.Name())
			files, fileErr := os.ReadDir(searchDir)
			if fileErr != nil {
				continue
			}
			for _, f := range files {
				if err := ctx.Err(); err != nil {
					return items, warnings, err
				}
				if f.IsDir() || !isModArchive(f.Name()) {
					continue
				}
				mirrorPath := filepath.Join(searchDir, f.Name())
				absPath, _ := filepath.Abs(mirrorPath)
				lowPath := strings.ToLower(filepath.Clean(absPath))
				if scanned[lowPath] {
					continue
				}
				scanned[lowPath] = true

				identity, inspectErr := inspectArchiveFile(absPath)
				if inspectErr != nil {
					warnings = append(warnings, fmt.Sprintf("inspect mirror %s: %v", absPath, inspectErr))
					continue
				}

				item := StorageAuditItem{
					ID:             stableAuditItemID("mirror", absPath),
					Path:           absPath,
					Purpose:        "collection-mirror",
					Classification: classCollectionMirror,
					LogicalBytes:   identity.SizeBytes,
					AllocatedBytes: identity.AllocatedBytes,
					LinkCount:      identity.Links,
					Identity:       identity,
				}

				ownedEntry, isOwned := ownedByTarget[lowPath]
				if !isOwned {
					item.Reason = "file in collection folder not tracked in ownership ledger"
					item.CleanupAllowed = false
					item.RecoveryAllowed = false
				} else if ownedEntry.State == archiveStatePendingRetire {
					item.EntityID = ownedEntry.EntityID
					item.ArtifactID = ownedEntry.ArtifactID
					item.SHA256 = ownedEntry.SHA256
					item.SourcePath = ownedEntry.SourcePath
					item.SourceIdentity = ownedEntry.SourceIdentity

					canonicalVerified := false
					if ownedEntry.SourcePath != "" {
						if _, statErr := os.Stat(ownedEntry.SourcePath); statErr == nil {
							canonicalVerified = true
						}
					}
					if identity.Links > 1 || canonicalVerified {
						item.Reason = "stale ledger-owned mirror entry pending retirement"
						item.CleanupAllowed = true
						if identity.Links <= 1 {
							item.ReclaimableBytes = identity.AllocatedBytes
							if !identity.AllocationKnown {
								item.ReclaimableBytes = identity.SizeBytes
							}
						}
					} else {
						item.Reason = "stale mirror but canonical source not verified; retained"
						item.CleanupAllowed = false
						item.RecoveryAllowed = true
					}
				} else if ownedEntry.State == archiveStateActive {
					item.EntityID = ownedEntry.EntityID
					item.ArtifactID = ownedEntry.ArtifactID
					item.SHA256 = ownedEntry.SHA256
					item.SourcePath = ownedEntry.SourcePath
					item.SourceIdentity = ownedEntry.SourceIdentity

					// Determine whether this active entry is still wanted.
					stale := false
					staleReason := ""

					// Check collection existence and entity membership.
					if ownedEntry.Purpose == archivePurposeCollection && ownedEntry.OwnerID != "" {
						live := checkCollectionLiveness(ownedEntry.OwnerID)
						if !live.exists {
							stale = true
							staleReason = "owner collection deleted"
						} else if ownedEntry.EntityID != "" && !live.entities[ownedEntry.EntityID] {
							stale = true
							staleReason = "entity no longer in collection's resolved selection"
						}
					}

					// Check source identity/artifact match.
					if !stale && ownedEntry.SourcePath != "" {
						srcIdentity, srcErr := inspectArchiveFile(ownedEntry.SourcePath)
						if srcErr != nil {
							stale = true
							staleReason = "source archive no longer accessible"
						} else if ownedEntry.SourceIdentity.IdentityKnown && srcIdentity.IdentityKnown {
							if srcIdentity.FileID != ownedEntry.SourceIdentity.FileID ||
								srcIdentity.VolumeID != ownedEntry.SourceIdentity.VolumeID {
								stale = true
								staleReason = "source archive identity changed (updated or replaced)"
							}
						}
					}

					if stale {
						canonicalVerified := false
						if ownedEntry.SourcePath != "" {
							if _, statErr := os.Stat(ownedEntry.SourcePath); statErr == nil {
								canonicalVerified = true
							}
						}
						if identity.Links > 1 || canonicalVerified {
							item.Reason = fmt.Sprintf("stale active mirror: %s", staleReason)
							item.CleanupAllowed = true
							if identity.Links <= 1 {
								item.ReclaimableBytes = identity.AllocatedBytes
								if !identity.AllocationKnown {
									item.ReclaimableBytes = identity.SizeBytes
								}
							}
						} else {
							item.Reason = fmt.Sprintf("stale mirror (%s) but canonical not verified; retained", staleReason)
							item.CleanupAllowed = false
							item.RecoveryAllowed = true
						}
					} else {
						item.Reason = "active owned collection mirror entry (current)"
						item.CleanupAllowed = false
					}
				} else {
					item.Reason = fmt.Sprintf("owned entry in state %s", ownedEntry.State)
					item.CleanupAllowed = false
				}

				if isOwned && !sameArchiveObject(identity,ownedEntry.TargetIdentity) {
					item.CleanupAllowed=false
					item.ReclaimableBytes=0
					item.RecoveryAllowed=true
					item.Reason="Archive changed outside Studio; recover the named file to preserve its contents and review ownership"
				}
				items = append(items, item)
				progress.Completed++
				progress.Current = f.Name()
				service.emitStorageProgress(*progress)
			}
		}
	}
	return items, warnings, nil
}

func (service *AppService) scanManagedDeployment(ctx context.Context, managedRoot string, ownedByTarget map[string]*OwnedArchiveEntry, sourceByPath map[string]*auditSourceRecord, scanned map[string]bool, progress *StorageProgress) ([]StorageAuditItem, []string, error) {
	var items []StorageAuditItem
	var warnings []string

	if _, err := os.Stat(managedRoot); err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}

	entries, err := os.ReadDir(managedRoot)
	if err != nil {
		return nil, nil, err
	}

	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return items, warnings, err
		}
		if entry.IsDir() || !isModArchive(entry.Name()) {
			continue
		}
		deployPath := filepath.Join(managedRoot, entry.Name())
		absPath, _ := filepath.Abs(deployPath)
		lowPath := strings.ToLower(filepath.Clean(absPath))
		if scanned[lowPath] {
			continue
		}
		scanned[lowPath] = true

		identity, inspectErr := inspectArchiveFile(absPath)
		if inspectErr != nil {
			warnings = append(warnings, fmt.Sprintf("inspect deployment %s: %v", absPath, inspectErr))
			continue
		}

		item := StorageAuditItem{
			ID:             stableAuditItemID("deploy", absPath),
			Path:           absPath,
			Purpose:        "deployment",
			Classification: classManagedDeployment,
			LogicalBytes:   identity.SizeBytes,
			AllocatedBytes: identity.AllocatedBytes,
			LinkCount:      identity.Links,
			Identity:       identity,
		}

		// Check if this is a ledger-owned managed entry.
		ownedEntry, isOwned := ownedByTarget[lowPath]

		// Also check if it's a hardlink to a known canonical source.
		isLinkedToSource := false
		if identity.IdentityKnown {
			for _, rec := range sourceByPath {
				srcPath := cleanOptionalPath(rec.path)
				if srcPath == "" {
					continue
				}
				srcIdentity, srcErr := inspectArchiveFile(srcPath)
				if srcErr != nil {
					continue
				}
				if srcIdentity.IdentityKnown &&
					srcIdentity.VolumeID == identity.VolumeID &&
					srcIdentity.FileID == identity.FileID {
					isLinkedToSource = true
					item.SourcePath = srcPath
					item.EntityID = rec.entityID
					item.ArtifactID = rec.artifactID
					item.SHA256 = rec.sha256
					item.SourceIdentity = srcIdentity
					break
				}
			}
		}

		if isOwned && (ownedEntry.State == archiveStateActive || ownedEntry.State == archiveStatePendingRetire) {
			item.EntityID = ownedEntry.EntityID
			item.ArtifactID = ownedEntry.ArtifactID
			item.SHA256 = ownedEntry.SHA256
			item.SourcePath = ownedEntry.SourcePath
			item.SourceIdentity = ownedEntry.SourceIdentity

			// Check whether canonical source is still accessible.
			sourceMissing := true
			if ownedEntry.SourcePath != "" {
				if _, statErr := os.Stat(ownedEntry.SourcePath); statErr == nil {
					sourceMissing = false
				}
			}

			if sourceMissing {
				// Canonical source missing — this managed copy may be the sole
				// surviving data. Allow recovery to re-establish canonical.
				item.Reason = "owned managed deployment but canonical source missing — recovery available"
				item.CleanupAllowed = false
				item.RecoveryAllowed = true
			} else if ownedEntry.State == archiveStatePendingRetire {
				item.Reason = "owned managed deployment pending retirement"
				item.CleanupAllowed = false
				item.RecoveryAllowed = false
			} else {
				item.Reason = "active owned managed deployment entry"
				item.CleanupAllowed = false
				item.RecoveryAllowed = false
			}
		} else if isLinkedToSource {
			item.Reason = "unowned archive linked to a canonical source; review recovery to adopt this named entry"
			item.CleanupAllowed = false
			item.RecoveryAllowed = true
		} else {
			// Unowned / unindexed ZIP in beamworlds-managed. Preserved by
			// default. Recovery is allowed: the user can adopt it into the
			// library via 'Recover to library and manage original'.
			item.Reason = "unindexed archive in managed directory — preserved; recovery available"
			item.CleanupAllowed = false
			item.RecoveryAllowed = true
		}

		if isOwned && !sameArchiveObject(identity,ownedEntry.TargetIdentity) {
			item.CleanupAllowed=false
			item.RecoveryAllowed=true
			item.Reason="Archive changed outside Studio; recover the named file before allowing Studio to manage it"
		}
		items = append(items, item)
		progress.Completed++
		progress.Current = entry.Name()
		service.emitStorageProgress(*progress)
	}
	return items, warnings, nil
}

// ---------------------------------------------------------------------------
// Allocation accounting
// ---------------------------------------------------------------------------

// buildStorageAudit produces the final audit with physical deduplication and
// allocation accounting. Fingerprint is built only from semantic snapshot
// fields — never from timestamps or random IDs.
func buildStorageAudit(items []StorageAuditItem, warnings []string) StorageAudit {
	sort.Slice(items, func(i, j int) bool {
		return items[i].Path < items[j].Path
	})

	for i := range items {
		if items[i].ID == "" {
			items[i].ID = stableAuditItemID("item", items[i].Path)
		}
	}

	audit := StorageAudit{
		CheckedAt: nowUTC(),
		Items:     items,
		Warnings:  warnings,
	}
	if audit.Items == nil {
		audit.Items = []StorageAuditItem{}
	}
	if audit.Warnings == nil {
		audit.Warnings = []string{}
	}

	// Physical identity deduplication for allocation accounting.
	seen := make(map[physicalFileKey]bool)
	seenRedundant := make(map[physicalFileKey]bool)
	seenRetained := make(map[physicalFileKey]bool)
	seenShared := make(map[physicalFileKey]bool)
	seenRequired := make(map[physicalFileKey]bool)

	for i := range items {
		item := &items[i]
		audit.ApparentBytes += item.LogicalBytes

		allocated := item.AllocatedBytes
		if !item.Identity.AllocationKnown {
			allocated = item.LogicalBytes
		}
		if !item.Identity.IdentityKnown || !item.Identity.AllocationKnown { audit.AllocationEstimated=true }

		key := physicalFileKey{volumeID: item.Identity.VolumeID, fileID: item.Identity.FileID}
		isPhysicalDuplicate := false
		if item.Identity.IdentityKnown && key.volumeID != "" && key.fileID != "" {
			if seen[key] {
				isPhysicalDuplicate = true
			}
			seen[key] = true
		}

		if !isPhysicalDuplicate {
			audit.UniqueAllocatedBytes += allocated
		} else if !seenShared[key] {
			audit.SharedBytes += allocated
			seenShared[key]=true
		}

		switch item.Classification {
		case classLegacyCacheRedundant:
			// Only count reclaimable bytes if this file ID hasn't been counted
			// already AND the link count is 1 (sole reference to this data).
			if item.Identity.Links <= 1 && item.ReclaimableBytes > 0 {
				if item.Identity.IdentityKnown && key.volumeID != "" && key.fileID != "" {
					if !seenRedundant[key] {
						seenRedundant[key] = true
						audit.RedundantBytes += item.ReclaimableBytes
					}
				} else {
					audit.RedundantBytes += item.ReclaimableBytes
				}
			}
		case classLegacyCacheOnly:
			if item.Identity.IdentityKnown && key.volumeID != "" && key.fileID != "" {
				if !seenRetained[key] {
					seenRetained[key] = true
					audit.RetainedBytes += allocated
				}
			} else {
				audit.RetainedBytes += allocated
			}
		case classManagedDeployment,classCollectionMirror:
			if item.RecoveryAllowed {
				if !item.Identity.IdentityKnown || !seenRetained[key] {audit.RetainedBytes+=allocated;seenRetained[key]=true}
			} else if item.CleanupAllowed {
				if !item.Identity.IdentityKnown || !seenRedundant[key] {audit.RedundantBytes+=item.ReclaimableBytes;seenRedundant[key]=true}
			} else if item.SourceIdentity.IdentityKnown && item.Identity.IdentityKnown && !sameArchiveFileID(item.SourceIdentity,item.Identity) && !seenRequired[key] {
				audit.RequiredCopyBytes+=allocated;seenRequired[key]=true
			}
		}
	}

	audit.Fingerprint = computeAuditFingerprint(items)
	return audit
}

// computeAuditFingerprint builds a deterministic fingerprint over semantic
// snapshot fields only — ID (derived from normalized path+purpose), path,
// classification, identity, size, ownership. Never includes checkedAt or
// random values so re-audits of identical filesystem state produce identical
// fingerprints.
func computeAuditFingerprint(items []StorageAuditItem) string {
	h := sha256.New()
	for _, item := range items {
		h.Write([]byte(item.ID))
		h.Write([]byte{0})
		h.Write([]byte(item.Path))
		h.Write([]byte{0})
		h.Write([]byte(item.Classification))
		h.Write([]byte{0})
		h.Write([]byte(item.EntityID))
		h.Write([]byte{0})
		h.Write([]byte(item.SHA256))
		h.Write([]byte{0})
		h.Write([]byte(item.Identity.VolumeID))
		h.Write([]byte{0})
		h.Write([]byte(item.Identity.FileID))
		h.Write([]byte{0})
		h.Write([]byte(item.Identity.ModifiedNs))
		h.Write([]byte{0})
		h.Write([]byte(fmt.Sprintf("%d", item.Identity.SizeBytes)))
		h.Write([]byte{0})
		h.Write([]byte(fmt.Sprintf("%d", item.Identity.Links)))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ---------------------------------------------------------------------------
// Cleanup implementation
// ---------------------------------------------------------------------------

func (service *AppService) cleanupOneItem(ctx context.Context, item *StorageAuditItem, result *StorageCleanupResult) error {
	current, exists, err := archiveIdentityIfPresent(item.Path)
	if err != nil { return err }
	if !exists || !sameArchiveObject(current,item.Identity) { return errors.New("archive changed or disappeared since the review") }
	switch item.Classification {
	case classLegacyCacheRedundant:
		return service.cleanupRedundantCache(ctx,item,current,result)
	case classCollectionMirror:
		owned,err:=service.store.listOwnedArchiveEntries(ctx);if err!=nil{return err}
		for _,entry:=range owned{
			if entry.Purpose==archivePurposeCollection && samePath(filepath.Join(entry.TargetRoot,entry.RelativePath),item.Path){
				if !sameArchiveObject(current,entry.TargetIdentity){return errors.New("collection mirror ownership changed")}
				if err:=service.retireCollectionMirrorEntry(ctx,entry);err!=nil{return err}
				if current.Links>1{result.RemovedLinks++}else{result.RemovedCopies++;result.ReclaimedBytes+=current.AllocatedBytes;result.ReclaimedEstimate=true}
				return nil
			}
		}
		return errors.New("collection archive is not owned; preserved")
	default:
		return fmt.Errorf("archive classification %q is protected",item.Classification)
	}
}

func (service *AppService) cleanupRedundantCache(ctx context.Context, item *StorageAuditItem, current ArchiveFileIdentity, result *StorageCleanupResult) error {
	if err:=validateArchiveChild(item.Path,filepath.Join(service.config.ProfileDir,legacyArchiveCacheDirectory));err!=nil{return err}
	sourcePath,err:=filepath.EvalSymlinks(item.SourcePath);if err!=nil{return err}
	targetPath,err:=filepath.EvalSymlinks(item.Path);if err!=nil{return err}
	if samePath(sourcePath,targetPath){return errors.New("refuse to remove a canonical archive")}
	source,exists,err:=archiveIdentityIfPresent(item.SourcePath);if err!=nil{return err}
	if !exists || !source.Regular{return errors.New("canonical source is unavailable")}
	shared:=sameArchiveFileID(source,current)
	if !shared{
		targetHash,err:=hashFileSHA256(ctx,item.Path);if err!=nil{return err}
		sourceHash,err:=hashFileSHA256(ctx,item.SourcePath);if err!=nil{return err}
		if targetHash!=sourceHash || (item.SHA256!="" && !strings.EqualFold(sourceHash,item.SHA256)){return errors.New("SHA256 mismatch; archive preserved")}
	}
	sourceAfter,exists,err:=archiveIdentityIfPresent(item.SourcePath);if err!=nil{return err}
	if !exists || !sameArchiveObject(source,sourceAfter){return errors.New("canonical source changed during verification")}
	targetAfter,exists,err:=archiveIdentityIfPresent(item.Path);if err!=nil{return err}
	if !exists || !sameArchiveObject(current,targetAfter){return errors.New("derived archive changed during verification")}
	if err:=ctx.Err();err!=nil{return err}
	if err:=service.requireGameStopped();err!=nil{return err}
	if err:=os.Remove(item.Path);err!=nil{return err}
	if shared || current.Links>1{result.RemovedLinks++}else{result.RemovedCopies++;result.ReclaimedBytes+=current.AllocatedBytes;result.ReclaimedEstimate=true}
	return nil
}


// ---------------------------------------------------------------------------
// SHA256 hashing
// ---------------------------------------------------------------------------

func hashFileSHA256(ctx context.Context, path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	buf := make([]byte, 256*1024)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, readErr := f.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", readErr
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ---------------------------------------------------------------------------
// Store helpers
// ---------------------------------------------------------------------------

// snapshotSourceCatalog reads ALL indexed archive links (active AND inactive)
// with their entity, artifact, and content identity information. Including
// inactive links ensures retained/archived versions are accounted for.
func (s *Store) snapshotSourceCatalog(ctx context.Context) ([]auditSourceRecord, error) {
	query := `SELECT l.id, l.entity_id, l.artifact_id, l.path, l.root_path,
		COALESCE(a.sha256,''), l.size_bytes, l.modified_at,
		COALESCE(e.display_name,''), COALESCE(e.archived_at,''), l.active
	FROM archive_links l
	JOIN entities e ON e.id = l.entity_id
	LEFT JOIN artifacts a ON a.id = l.artifact_id
	ORDER BY l.entity_id, l.active DESC, l.last_seen_at DESC`

	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []auditSourceRecord
	for rows.Next() {
		var rec auditSourceRecord
		var activeInt int
		if err := rows.Scan(&rec.linkID, &rec.entityID, &rec.artifactID,
			&rec.path, &rec.rootPath, &rec.sha256, &rec.sizeBytes,
			&rec.modifiedAt, &rec.displayName, &rec.archivedAt, &activeInt); err != nil {
			return nil, err
		}
		rec.active = activeInt != 0
		records = append(records, rec)
	}
	return records, rows.Err()
}

func (s *Store) snapshotSourceCatalogForEntities(ctx context.Context, entityIDs []string) ([]auditSourceRecord, error) {
	if len(entityIDs) == 0 {
		return nil, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(entityIDs)), ",")
	query := `SELECT l.id, l.entity_id, l.artifact_id, l.path, l.root_path,
		COALESCE(a.sha256,''), l.size_bytes, l.modified_at,
		COALESCE(e.display_name,''), COALESCE(e.archived_at,''), l.active
	FROM archive_links l
	JOIN entities e ON e.id = l.entity_id
	LEFT JOIN artifacts a ON a.id = l.artifact_id
	WHERE l.entity_id IN (` + placeholders + `)
	ORDER BY l.entity_id, l.active DESC, l.last_seen_at DESC`

	args := make([]any, len(entityIDs))
	for i, id := range entityIDs {
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []auditSourceRecord
	for rows.Next() {
		var rec auditSourceRecord
		var activeInt int
		if err := rows.Scan(&rec.linkID, &rec.entityID, &rec.artifactID,
			&rec.path, &rec.rootPath, &rec.sha256, &rec.sizeBytes,
			&rec.modifiedAt, &rec.displayName, &rec.archivedAt, &activeInt); err != nil {
			return nil, err
		}
		rec.active = activeInt != 0
		records = append(records, rec)
	}
	return records, rows.Err()
}

func (service *AppService) finalizeStorageOperation(ctx context.Context,journalID string,items []storageCleanupJournalEntry,result *StorageCleanupResult,progress *StorageProgress,resultErr *error){
	if *resultErr!=nil{result.Failures=append(result.Failures,(*resultErr).Error())}
	if journalID!=""{
		finalCtx,cancel:=context.WithTimeout(context.WithoutCancel(ctx),10*time.Second);defer cancel()
		state:=journalStateDone;if len(result.Failures)>0{state=journalStateFailed}
		payload,err:=json.Marshal(items)
		if err==nil{err=service.store.completeDeploymentJournal(finalCtx,journalID,state,string(payload))}
		if err!=nil{result.Failures=append(result.Failures,"Persist operation result: "+err.Error());*resultErr=errors.Join(*resultErr,err)}
	}
	progress.Phase="complete";progress.Done=true;progress.Result=result
	if *resultErr!=nil{progress.Error=(*resultErr).Error()}
	service.emitStorageProgress(*progress)
}
