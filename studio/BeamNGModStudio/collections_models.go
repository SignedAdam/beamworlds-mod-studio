package main

import modkit "github.com/SignedAdam/beamworlds-modkit"

type CollectionCoverImage struct {
	AssetID string  `json:"assetId"`
	FocalX  float64 `json:"focalX"`
	FocalY  float64 `json:"focalY"`
}

type CollectionCover struct {
	Mode   string                 `json:"mode"`
	Images []CollectionCoverImage `json:"images"`
}

type CollectionArtworkCandidate struct {
	Path    string `json:"path"`
	Name    string `json:"name"`
	URL     string `json:"url"`
	AssetID string `json:"assetId"`
}

type ModCollection struct {
	ID                 string          `json:"id"`
	Name               string          `json:"name"`
	Description        string          `json:"description"`
	UpdatedAt          string          `json:"updatedAt"`
	Position           int             `json:"position"`
	ModCount           int             `json:"modCount"`
	ArchivedModCount   int             `json:"archivedModCount"`
	DirectModCount     int             `json:"directModCount"`
	DirectEnabledCount int             `json:"directEnabledCount"`
	ChildCount         int             `json:"childCount"`
	ChildIDs           []string        `json:"childIds"`
	ParentIDs          []string        `json:"parentIds"`
	Cover              CollectionCover `json:"cover"`
	CoverURL           string          `json:"coverUrl"`
}

type CollectionReference struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type CollectionUsage struct {
	Collections []CollectionReference `json:"collections"`
	Profiles    []CollectionReference `json:"profiles"`
}

type CollectionMod struct {
	EntityID      string      `json:"entityId"`
	DisplayName   string      `json:"displayName"`
	Kind          modkit.Kind `json:"kind"`
	ArchivePath   string      `json:"archivePath"`
	ArchivedAt    string      `json:"archivedAt"`
	SHA256        string      `json:"sha256"`
	SizeBytes     int64       `json:"sizeBytes"`
	ThumbnailURL  string      `json:"thumbnailUrl"`
	Available     bool        `json:"available"`
	CollectionIDs []string    `json:"collectionIds"`
	RootIDs       []string    `json:"rootIds"`
}

// A membership row: the mod stays in the collection when disabled, it just
// stops contributing to a resolved selection. DisabledByArchive marks
// memberships that were auto-disabled when the mod was archived; restoring
// the mod re-enables only these, leaving user-disabled memberships untouched.
type CollectionMember struct {
	EntityID          string `json:"entityId"`
	Enabled           bool   `json:"enabled"`
	DisabledByArchive bool   `json:"disabledByArchive"`
}

// A parent -> child edge. Disabling it stops traversal through this edge only;
// the child still contributes anywhere else it is reached.
type CollectionChild struct {
	CollectionID string `json:"collectionId"`
	Enabled      bool   `json:"enabled"`
}

type CollectionDetail struct {
	Collection ModCollection      `json:"collection"`
	Members    []CollectionMember `json:"members"`
	Children   []CollectionChild  `json:"children"`
	Mods       []CollectionMod    `json:"mods"`
	Usage      CollectionUsage    `json:"usage"`
}

type ModProfile struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	UpdatedAt       string   `json:"updatedAt"`
	CollectionIDs   []string `json:"collectionIds"`
	CollectionCount int      `json:"collectionCount"`
	ModCount        int      `json:"modCount"`
}

type OrganizationState struct {
	Collections []ModCollection `json:"collections"`
	Tags        []ModTag        `json:"tags"`
	Profiles    []ModProfile    `json:"profiles"`
}

type PlaySelection struct {
	CollectionIDs         []string        `json:"collectionIds"`
	IncludedCollectionIDs []string        `json:"includedCollectionIds"`
	Mods                  []CollectionMod `json:"mods"`
	ModCount              int             `json:"modCount"`
	MissingCount          int             `json:"missingCount"`
	ArchivedCount         int             `json:"archivedCount"`
	Fingerprint           string          `json:"fingerprint"`
	Warnings              []string        `json:"warnings"`
}

type PlayState struct {
	ProfileID            string   `json:"profileId"`
	CollectionIDs        []string `json:"collectionIds"`
	DefaultCollectionIDs []string `json:"defaultCollectionIds"`
	Notices              []string `json:"notices"`
}

type PlayRequest struct {
	CollectionIDs []string `json:"collectionIds"`
	Fingerprint   string   `json:"fingerprint"`
}

type PlayProgress struct {
	OperationID string `json:"operationId"`
	Phase       string `json:"phase"`
	Current     string `json:"current"`
	Error       string `json:"error"`
	Completed   int    `json:"completed"`
	Total       int    `json:"total"`
	BytesCopied int64  `json:"bytesCopied"`
	TotalBytes  int64  `json:"totalBytes"`
	Done        bool   `json:"done"`
}

type PlayActivation struct {
	OperationID   string   `json:"operationId"`
	ModCount      int      `json:"modCount"`
	UserPath      string   `json:"userPath"`
	ModsPath      string   `json:"modsPath"`
	ActivatedAt   string   `json:"activatedAt"`
	CollectionIDs []string `json:"collectionIds"`
	Fingerprint   string   `json:"fingerprint"`
}

type PlayResult struct {
	Applied          bool           `json:"applied"`
	Started          bool           `json:"started"`
	Activation       PlayActivation `json:"activation"`
	Process          ProcessLaunch  `json:"process"`
	Error            string         `json:"error"`
	ProcessUncertain bool           `json:"processUncertain"`
}

type PlayRuntimeState struct {
	Applied     bool           `json:"applied"`
	Activation  PlayActivation `json:"activation"`
	GameRunning bool           `json:"gameRunning"`
	Warning     string         `json:"warning"`
}
