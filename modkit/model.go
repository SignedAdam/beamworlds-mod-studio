package modkit

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

const (
	SchemaVersion   = 1
	AnalyzerVersion = "0.2.0"
)

type Kind string

const (
	KindVehicle Kind = "vehicle"
	KindMap     Kind = "map"
	KindUI      Kind = "ui"
	KindScript  Kind = "script"
	KindMixed   Kind = "mixed"
	KindUnknown Kind = "unknown"
)

type Severity string

const (
	SeverityInfo    Severity = "info"
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

type Issue struct {
	Code     string   `json:"code"`
	Severity Severity `json:"severity"`
	Message  string   `json:"message"`
	Path     string   `json:"path,omitempty"`
}

type ArchiveMember struct {
	Path              string    `json:"path"`
	CompressedBytes   uint64    `json:"compressedBytes"`
	UncompressedBytes uint64    `json:"uncompressedBytes"`
	CRC32             uint32    `json:"crc32"`
	Method            uint16    `json:"method"`
	ModifiedAt        time.Time `json:"modifiedAt,omitempty"`
	Directory         bool      `json:"directory"`
}

type MetadataDocument struct {
	Path string         `json:"path"`
	Data map[string]any `json:"data"`
}

type ImageCandidate struct {
	Path              string `json:"path"`
	Role              string `json:"role"`
	UncompressedBytes uint64 `json:"uncompressedBytes"`
	Width             int    `json:"width,omitempty"`
	Height            int    `json:"height,omitempty"`
	MIME              string `json:"mime,omitempty"`
	AssetID           string `json:"assetId,omitempty"`
}

type Variant struct {
	Namespace     string         `json:"namespace"`
	BaseName      string         `json:"baseName"`
	ConfigPath    string         `json:"configPath"`
	MetadataPath  string         `json:"metadataPath,omitempty"`
	ThumbnailPath string         `json:"thumbnailPath,omitempty"`
	Configuration string         `json:"configuration,omitempty"`
	Description   string         `json:"description,omitempty"`
	ConfigType    string         `json:"configType,omitempty"`
	BodyStyle     string         `json:"bodyStyle,omitempty"`
	Drivetrain    string         `json:"drivetrain,omitempty"`
	Transmission  string         `json:"transmission,omitempty"`
	FuelType      string         `json:"fuelType,omitempty"`
	Propulsion    string         `json:"propulsion,omitempty"`
	Power         float64        `json:"power,omitempty"`
	Torque        float64        `json:"torque,omitempty"`
	Weight        float64        `json:"weight,omitempty"`
	Value         float64        `json:"value,omitempty"`
	TopSpeed      float64        `json:"topSpeed,omitempty"`
	Fields        map[string]any `json:"fields,omitempty"`
}

type JBeamStats struct {
	Files             int      `json:"files"`
	ParsedFiles       int      `json:"parsedFiles"`
	DeclaredNodes     int      `json:"declaredNodes"`
	DeclaredBeams     int      `json:"declaredBeams"`
	DeclaredTriangles int      `json:"declaredTriangles"`
	DeclaredSlots     int      `json:"declaredSlots"`
	DeclaredHydros    int      `json:"declaredHydros"`
	Controllers       []string `json:"controllers,omitempty"`
}

type MapStats struct {
	LevelIDs         []string `json:"levelIds,omitempty"`
	TerrainFiles     int      `json:"terrainFiles"`
	LevelObjectFiles int      `json:"levelObjectFiles"`
	ForestFiles      int      `json:"forestFiles"`
	FacilityFiles    int      `json:"facilityFiles"`
	MaterialFiles    int      `json:"materialFiles"`
	ModelFiles       int      `json:"modelFiles"`
	TextureFiles     int      `json:"textureFiles"`
	SpawnPoints      int      `json:"spawnPoints"`
}

type UIStats struct {
	AppRoots        []string `json:"appRoots,omitempty"`
	HTMLFiles       int      `json:"htmlFiles"`
	CSSFiles        int      `json:"cssFiles"`
	JavaScriptFiles int      `json:"javaScriptFiles"`
	LuaFiles        int      `json:"luaFiles"`
	SettingsFiles   int      `json:"settingsFiles"`
	ScriptFiles     int      `json:"scriptFiles"`
}

type Manifest struct {
	SchemaVersion      int                 `json:"schemaVersion"`
	AnalyzerVersion    string              `json:"analyzerVersion"`
	AnalyzedAt         time.Time           `json:"analyzedAt"`
	ArchivePath        string              `json:"archivePath"`
	Filename           string              `json:"filename"`
	SizeBytes          int64               `json:"sizeBytes"`
	ModifiedAt         time.Time           `json:"modifiedAt"`
	CentralFingerprint string              `json:"centralFingerprint"`
	FullSHA256         string              `json:"fullSha256,omitempty"`
	ValidArchive       bool                `json:"validArchive"`
	Wrapper            string              `json:"wrapper,omitempty"`
	Kind               Kind                `json:"kind"`
	ContentTags        []string            `json:"contentTags,omitempty"`
	Namespaces         map[string][]string `json:"namespaces"`
	Title              string              `json:"title,omitempty"`
	Description        string              `json:"description,omitempty"`
	Author             string              `json:"author,omitempty"`
	Version            string              `json:"version,omitempty"`
	EntryCount         int                 `json:"entryCount"`
	CompressedBytes    uint64              `json:"compressedBytes"`
	UncompressedBytes  uint64              `json:"uncompressedBytes"`
	Members            []ArchiveMember     `json:"members"`
	MetadataDocuments  []MetadataDocument  `json:"metadataDocuments,omitempty"`
	Images             []ImageCandidate    `json:"images,omitempty"`
	SelectedImagePath  string              `json:"selectedImagePath,omitempty"`
	Variants           []Variant           `json:"variants,omitempty"`
	JBeam              JBeamStats          `json:"jbeam"`
	Map                MapStats            `json:"map"`
	UI                 UIStats             `json:"ui"`
	Issues             []Issue             `json:"issues,omitempty"`
}

type FileSnapshot struct {
	Path       string `json:"path"`
	SHA256     string `json:"sha256"`
	SizeBytes  int64  `json:"sizeBytes"`
	ModifiedNS int64  `json:"modifiedNs"`
}

type WorkspaceManifest struct {
	ID                string         `json:"id"`
	EntityID          string         `json:"entityId"`
	ArtifactID        string         `json:"artifactId"`
	SourceFingerprint string         `json:"sourceFingerprint"`
	CreatedAt         time.Time      `json:"createdAt"`
	Kind              Kind           `json:"kind"`
	Files             []FileSnapshot `json:"files"`
}

type WorkspaceChange struct {
	Path      string `json:"path"`
	Type      string `json:"type"`
	BeforeSHA string `json:"beforeSha,omitempty"`
	AfterSHA  string `json:"afterSha,omitempty"`
	SizeBytes int64  `json:"sizeBytes,omitempty"`
	TextDiff  string `json:"textDiff,omitempty"`
}

type ValidationResult struct {
	Valid  bool    `json:"valid"`
	Issues []Issue `json:"issues"`
}

func NewID() (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("generate UUIDv7: %w", err)
	}
	return id.String(), nil
}
