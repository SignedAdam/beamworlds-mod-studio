package main

// Archive deployment models implement the typed contracts from spec 049.
// JSON field names are authoritative for frontend bindings.

// legacyArchiveCacheDirectory is the old profiles/.archive-cache path that the
// pre-049 launch used as a permanent content-addressed ZIP cache. New launch
// paths never read or write this directory; it exists only for migration and
// recovery.
const legacyArchiveCacheDirectory = ".archive-cache"

// ArchiveDeploymentMode values.
const (
	DeploymentModeAuto        = "auto"
	DeploymentModeHardlinkOnly = "hardlink-only"
	DeploymentModeCopy        = "copy"
)

// ArchiveFileIdentity captures authoritative volume, file identity, size, and
// allocation information from the platform. IDs are opaque strings so 64/128-
// bit identities survive JSON and JavaScript without precision loss.
type ArchiveFileIdentity struct {
	VolumeID        string `json:"volumeId"`
	FileID          string `json:"fileId"`
	SizeBytes       int64  `json:"sizeBytes"`
	AllocatedBytes  int64  `json:"allocatedBytes"`
	AllocationKnown bool   `json:"allocationKnown"`
	IdentityKnown   bool   `json:"identityKnown"`
	Links           int    `json:"links"`
	ModifiedNs      string `json:"modifiedNs"`
	Regular         bool   `json:"regular"`
}

// ArchiveCapability records the result of probing a source→destination pair
// for hardlink support and copy feasibility.
type ArchiveCapability struct {
	SourceRoot         string `json:"sourceRoot"`
	DestinationRoot    string `json:"destinationRoot"`
	SourceVolumeID     string `json:"sourceVolumeId"`
	DestinationVolumeID string `json:"destinationVolumeId"`
	Hardlinks          bool   `json:"hardlinks"`
	Checked            bool   `json:"checked"`
	CopyPossible       bool   `json:"copyPossible"`
	ReasonCode         string `json:"reasonCode"`
	Reason             string `json:"reason"`
	CheckedAt          string `json:"checkedAt"`
	FreeBytes          int64  `json:"freeBytes"`
}

// ArchiveDeploymentState reports the current deployment policy and capabilities.
type ArchiveDeploymentState struct {
	Mode         string              `json:"mode"`
	Capabilities []ArchiveCapability `json:"capabilities"`
	Mixed        bool                `json:"mixed"`
	Warning      string              `json:"warning"`
}

// ArchiveDeploymentEntry describes one archive in a deployment plan or result.
type ArchiveDeploymentEntry struct {
	EntityID        string              `json:"entityId"`
	ArtifactID      string              `json:"artifactId"`
	SourcePath      string              `json:"sourcePath"`
	DestinationPath string              `json:"destinationPath"`
	ReusePath       string              `json:"reusePath,omitempty"`
	SHA256          string              `json:"sha256"`
	Method          string              `json:"method"`
	Reuse           bool                `json:"reuse"`
	VerifySource    bool                `json:"verifySource"`
	SizeBytes       int64               `json:"sizeBytes"`
	SourceIdentity  ArchiveFileIdentity `json:"sourceIdentity"`
	TargetIdentity  ArchiveFileIdentity `json:"targetIdentity"`
}

// ArchiveDeploymentPlan is the reviewed, fingerprinted plan for deploying a set
// of archives into the game's managed directory.
type ArchiveDeploymentPlan struct {
	operationID                string
	Fingerprint                string                   `json:"fingerprint"`
	SelectionFingerprint       string                   `json:"selectionFingerprint"`
	Mode                       string                   `json:"mode"`
	Purpose                    string                   `json:"purpose"`
	OwnerID                    string                   `json:"ownerId"`
	DestinationRoot            string                   `json:"destinationRoot"`
	CollectionIDs              []string                 `json:"collectionIds"`
	ExcludedCollectionIDs      []string                 `json:"excludedCollectionIds"`
	Entries                    []ArchiveDeploymentEntry `json:"entries"`
	Capabilities               []ArchiveCapability      `json:"capabilities"`
	CopyBytes                  int64                    `json:"copyBytes"`
	HashBytes                  int64                    `json:"hashBytes"`
	Retiring                   []OwnedArchiveEntry      `json:"retiring"`
	OwnershipFingerprint       string                   `json:"ownershipFingerprint"`
	PeakBytes                  int64                    `json:"peakBytes"`
	AdditionalBytes            int64                    `json:"additionalBytes"`
	ReusedCount                int                      `json:"reusedCount"`
	LinkedCount                int                      `json:"linkedCount"`
	RequiresCopyConfirmation   bool                     `json:"requiresCopyConfirmation"`
	Merge                      bool                     `json:"merge"`
	Blockers                   []string                 `json:"blockers"`
}

// ArchiveDeploymentResult reports the outcome of applying a deployment plan.
type ArchiveDeploymentResult struct {
	OperationID string                   `json:"operationId"`
	Entries     []ArchiveDeploymentEntry `json:"entries"`
	Linked      int                      `json:"linked"`
	Copied      int                      `json:"copied"`
	Reused      int                      `json:"reused"`
	Removed     int                      `json:"removed"`
	CopiedBytes int64                    `json:"copiedBytes"`
	HashedBytes int64                    `json:"hashedBytes"`
}

// OwnedArchiveEntry records an app-owned generated file with its purpose,
// source identity, and lifecycle state. Ownership records survive source
// disappearance and must not be cascade-deleted with entity/artifact removal.
type OwnedArchiveEntry struct {
	ID             string              `json:"id"`
	Purpose        string              `json:"purpose"`
	OwnerID        string              `json:"ownerId"`
	EntityID       string              `json:"entityId"`
	ArtifactID     string              `json:"artifactId"`
	SHA256         string              `json:"sha256"`
	SourcePath     string              `json:"sourcePath"`
	TargetRoot     string              `json:"targetRoot"`
	RelativePath   string              `json:"relativePath"`
	Method         string              `json:"method"`
	State          string              `json:"state"`
	SourceIdentity ArchiveFileIdentity `json:"sourceIdentity"`
	TargetIdentity ArchiveFileIdentity `json:"targetIdentity"`
}

// Deployment journal states.
const (
	journalStatePlanned    = "planned"
	journalStatePrepared   = "prepared"
	journalStateActivating = "activating"
	journalStateApplied    = "applied"
	journalStateCleanup    = "cleanup-pending"
	journalStateDone       = "done"
	journalStateFailed     = "failed"
)

// Deployment entry methods.
const (
	deployMethodHardlink = "hardlink"
	deployMethodCopy     = "copy"
	deployMethodJunction = "junction"
)

// Owned archive entry purposes.
const (
	archivePurposePlay          = "play"
	archivePurposeCollection    = "collection"
	archivePurposeMigration     = "migration"
	archivePurposeTestInstall   = "test-install"
)

// Stable logical owner IDs for deployment plans. These are not per-operation
// UUIDs; they identify the logical owner so replanning identical state yields
// the same fingerprint. OperationID is generated at apply time.
const playDeploymentOwnerID = "active-play"

// Owned archive entry lifecycle states.
const (
	archiveStateActive          = "active"
	archiveStatePendingRetire   = "pending-retire"
	archiveStateRetired         = "retired"
)

// Capability reason codes.
const (
	capReasonSameVolume       = "same-volume"
	capReasonDifferentVolume  = "different-volume"
	capReasonUnsupported      = "filesystem-unsupported"
	capReasonPermission       = "permission-denied"
	capReasonDriveUnavailable = "drive-unavailable"
	capReasonNotChecked       = "not-checked"
	capReasonReadOnlySource   = "read-only-source"
	capReasonUnknown          = "unknown"
	capReasonIOError          = "io-error"
)

// ValidDeploymentMode returns true if the value is a recognized deployment mode.
func ValidDeploymentMode(mode string) bool {
	switch mode {
	case DeploymentModeAuto, DeploymentModeHardlinkOnly, DeploymentModeCopy:
		return true
	}
	return false
}
