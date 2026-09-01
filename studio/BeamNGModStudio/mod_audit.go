package main

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	modkit "github.com/SignedAdam/beamworlds-modkit"
)

const (
	maxAuditEntryRead   = 256 << 10
	maxAuditExcerpt     = 32 << 10
	maxAuditTotalRead   = 48 << 20
	maxAuditArtifacts   = 800
	maxAuditSurfaceRows = 240
)

type ModAuditSignal struct {
	Severity string `json:"severity"`
	Category string `json:"category"`
	Code     string `json:"code"`
	Path     string `json:"path"`
	Detail   string `json:"detail"`
	Evidence string `json:"evidence"`
}

type ModAuditLocalReport struct {
	ScannedEntries  int              `json:"scannedEntries"`
	CandidateFiles  int              `json:"candidateFiles"`
	ExecutableFiles int              `json:"executableFiles"`
	SuspiciousFiles int              `json:"suspiciousFiles"`
	ContentSignals  int              `json:"contentSignals"`
	BytesInspected  int64            `json:"bytesInspected"`
	Truncated       bool             `json:"truncated"`
	Signals         []ModAuditSignal `json:"signals"`
}

type ModAuditAttackSurfaceEntry struct {
	Path               string           `json:"path"`
	Type               string           `json:"type"`
	Reason             string           `json:"reason"`
	LibraryOccurrences int              `json:"libraryOccurrences"`
	LibraryMods        int              `json:"libraryMods"`
	Novel              bool             `json:"novel"`
	Signals            []ModAuditSignal `json:"signals"`
}

type ModAuditAttackSurface struct {
	LibraryMods int                          `json:"libraryMods"`
	Patterns    map[string]int               `json:"patterns"`
	Entries     []ModAuditAttackSurfaceEntry `json:"entries"`
}

type ModAuditPreScanFile struct {
	Path         string   `json:"path"`
	Observations []string `json:"observations"`
	Behaviors    []string `json:"behaviors"`
	FollowUp     []string `json:"followUp"`
}

type ModAuditPreScanReport struct {
	Model     string                `json:"model"`
	Reasoning string                `json:"reasoning"`
	Summary   string                `json:"summary"`
	Files     []ModAuditPreScanFile `json:"files"`
	Raw       string                `json:"raw"`
}

type ModAuditFinding struct {
	Severity       string `json:"severity"`
	Title          string `json:"title"`
	Path           string `json:"path"`
	Evidence       string `json:"evidence"`
	Impact         string `json:"impact"`
	Recommendation string `json:"recommendation"`
}

type ModAuditFinalReport struct {
	Model          string            `json:"model"`
	Reasoning      string            `json:"reasoning"`
	OverallRisk    string            `json:"overallRisk"`
	Summary        string            `json:"summary"`
	Findings       []ModAuditFinding `json:"findings"`
	ContentSignals []ModAuditFinding `json:"contentSignals"`
	FollowUpPaths  []string          `json:"followUpPaths"`
	FocusedPaths   []string          `json:"focusedPaths"`
	Warnings       []string          `json:"warnings"`
	Raw            string            `json:"raw"`
}

type ModAuditFollowUp struct {
	Question  string   `json:"question"`
	Paths     []string `json:"paths"`
	Response  string   `json:"response"`
	CreatedAt string   `json:"createdAt"`
}

type ModAuditFileArtifact struct {
	Path           string               `json:"path"`
	Fingerprint    string               `json:"fingerprint"`
	SizeBytes      int64                `json:"sizeBytes"`
	EntrypointType string               `json:"entrypointType"`
	MediaType      string               `json:"mediaType"`
	Signals        []ModAuditSignal     `json:"signals"`
	PreScan        *ModAuditPreScanFile `json:"preScan,omitempty"`
}

type ModAudit struct {
	ID            string                 `json:"id"`
	EntityID      string                 `json:"entityId"`
	ArtifactID    string                 `json:"artifactId"`
	Status        string                 `json:"status"`
	Stage         string                 `json:"stage"`
	CreatedAt     string                 `json:"createdAt"`
	UpdatedAt     string                 `json:"updatedAt"`
	Deterministic ModAuditLocalReport    `json:"deterministic"`
	AttackSurface ModAuditAttackSurface  `json:"attackSurface"`
	Files         []ModAuditFileArtifact `json:"files"`
	PreScan       ModAuditPreScanReport  `json:"preScan"`
	Final         ModAuditFinalReport    `json:"final"`
	FollowUps     []ModAuditFollowUp     `json:"followUps"`
	Error         string                 `json:"error"`
}

type modAuditArtifactRecord struct {
	ModAuditFileArtifact
	Excerpt string
}

type auditBaseline struct {
	mods     int
	patterns map[string]int
}

type auditPatternSignal struct {
	code     string
	category string
	severity string
	detail   string
	needles  []string
}

var auditTextPatterns = []auditPatternSignal{
	{code: "process_launch", category: "process", severity: "high", detail: "Can launch an external process", needles: []string{"os.execute(", "io.popen(", "child_process", "cmd.exe", "powershell", "process.start("}},
	{code: "native_library_load", category: "native", severity: "high", detail: "Can load native code", needles: []string{"package.loadlib", "ffi.load(", "loadlibrary("}},
	{code: "dynamic_code", category: "execution", severity: "high", detail: "Can construct or evaluate code dynamically", needles: []string{"loadstring(", "eval(", "new function("}},
	{code: "network_access", category: "network", severity: "medium", detail: "Can communicate outside the game", needles: []string{"http://", "https://", "websocket", "xmlhttprequest", "socket.", "fetch("}},
	{code: "filesystem_mutation", category: "filesystem", severity: "medium", detail: "Can modify or remove host files", needles: []string{"io.open(", "os.remove(", "os.rename(", "fs.writefile", "fs.unlink", "file.delete("}},
	{code: "persistence_mechanism", category: "persistence", severity: "high", detail: "References an operating-system persistence location", needles: []string{"currentversion\\run", "startup\\programs", "schtasks", "crontab"}},
	{code: "credential_access", category: "credentials", severity: "critical", detail: "References credential or browser-session storage", needles: []string{"login data", "cookies.sqlite", ".ssh/id_rsa", "local state", "discord token"}},
	{code: "destructive_command", category: "destructive", severity: "critical", detail: "References a destructive host command", needles: []string{"format c:", "rm -rf /", "cipher /w:", "vssadmin delete shadows"}},
}

var auditLongEncodedText = regexp.MustCompile(`[A-Za-z0-9+/]{240,}={0,2}`)

var auditExecutableExtensions = map[string]bool{
	".bat": true, ".cmd": true, ".com": true, ".dll": true, ".exe": true,
	".jar": true, ".msi": true, ".ps1": true, ".scr": true, ".vbs": true,
}

var auditImageMediaTypes = map[string]string{
	".gif": "image/gif", ".jpeg": "image/jpeg", ".jpg": "image/jpeg",
	".png": "image/png", ".webp": "image/webp",
}

var auditTextExtensions = map[string]bool{
	".bat": true, ".cfg": true, ".cmd": true, ".cs": true, ".css": true,
	".html": true, ".ini": true, ".jbeam": true, ".js": true, ".json": true,
	".json5": true, ".lua": true, ".md": true, ".mis": true, ".pc": true,
	".ps1": true, ".py": true, ".sh": true, ".ts": true, ".txt": true,
	".vbs": true, ".xml": true, ".yaml": true, ".yml": true,
}

func (service *AppService) GetModAudit(entityID string) (ModAudit, error) {
	ctx := context.Background()
	item, err := service.store.GetLibraryItem(ctx, strings.TrimSpace(entityID))
	if err != nil {
		return ModAudit{}, err
	}
	audit, err := service.store.latestModAudit(ctx, item.EntityID, item.ArtifactID)
	if errors.Is(err, sql.ErrNoRows) {
		return ModAudit{EntityID: item.EntityID, ArtifactID: item.ArtifactID, Status: "not_started", Stage: "none", Files: []ModAuditFileArtifact{}, FollowUps: []ModAuditFollowUp{}}, nil
	}
	return audit, err
}

func (service *AppService) RunModAuditLocal(entityID string) (ModAudit, error) {
	service.auditMu.Lock()
	defer service.auditMu.Unlock()
	return service.runModAuditLocalLocked(context.Background(), strings.TrimSpace(entityID), true)
}

func (service *AppService) runModAuditLocalLocked(ctx context.Context, entityID string, force bool) (ModAudit, error) {
	item, err := service.store.GetLibraryItem(ctx, entityID)
	if err != nil {
		return ModAudit{}, err
	}
	if !item.Linked || strings.TrimSpace(item.ArchivePath) == "" {
		return ModAudit{}, errors.New("Mod Audit requires a linked source archive")
	}
	if !force {
		if audit, loadErr := service.store.latestModAudit(ctx, item.EntityID, item.ArtifactID); loadErr == nil && audit.Deterministic.ScannedEntries > 0 {
			return audit, nil
		} else if loadErr != nil && !errors.Is(loadErr, sql.ErrNoRows) {
			return ModAudit{}, loadErr
		}
	}
	baseline, err := service.learnModAuditBaseline(ctx)
	if err != nil {
		return ModAudit{}, err
	}
	local, surface, artifacts, err := scanArchiveForModAudit(item.ArchivePath, baseline)
	if err != nil {
		return ModAudit{}, err
	}
	auditID, err := modkit.NewID()
	if err != nil {
		return ModAudit{}, err
	}
	now := nowUTC()
	audit := ModAudit{
		ID: auditID, EntityID: item.EntityID, ArtifactID: item.ArtifactID,
		Status: "local_complete", Stage: "local", CreatedAt: now, UpdatedAt: now,
		Deterministic: local, AttackSurface: surface,
	}
	if err := service.store.createModAudit(ctx, audit, artifacts); err != nil {
		return ModAudit{}, err
	}
	_ = service.store.AppendEvent(ctx, item.EntityID, "mod_audit_local_complete", map[string]any{"auditId": auditID, "signals": len(local.Signals), "entrypoints": len(surface.Entries)})
	return service.store.modAuditByID(ctx, auditID)
}

func (service *AppService) learnModAuditBaseline(ctx context.Context) (auditBaseline, error) {
	items, err := service.store.ListLibrary(ctx, "all", "all", "", "all")
	if err != nil {
		return auditBaseline{}, err
	}
	baseline := auditBaseline{mods: len(items), patterns: map[string]int{}}
	for _, item := range items {
		seen := map[string]bool{}
		for _, member := range item.Manifest.Members {
			entryType, _ := classifyAuditEntrypoint(member.Path)
			if entryType != "" {
				seen[entryType] = true
			}
		}
		for pattern := range seen {
			baseline.patterns[pattern]++
		}
	}
	return baseline, nil
}

func scanArchiveForModAudit(archivePath string, baseline auditBaseline) (ModAuditLocalReport, ModAuditAttackSurface, []modAuditArtifactRecord, error) {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return ModAuditLocalReport{}, ModAuditAttackSurface{}, nil, fmt.Errorf("open source archive for Mod Audit: %w", err)
	}
	defer reader.Close()

	report := ModAuditLocalReport{Signals: []ModAuditSignal{}}
	artifacts := make([]modAuditArtifactRecord, 0, min(len(reader.File), maxAuditArtifacts))
	artifactIndexes := make(map[string]int, min(len(reader.File), maxAuditArtifacts))
	canonicalPaths := make(map[string]string, min(len(reader.File), maxAuditArtifacts))
	var inspected int64
	for _, file := range reader.File {
		if file.FileInfo().IsDir() {
			continue
		}
		report.ScannedEntries++
		path := normalizeAuditPath(file.Name)
		artifactKey := strings.ToLower(path)
		canonicalPath, duplicatePath := canonicalPaths[artifactKey]
		if !duplicatePath {
			canonicalPath = path
			canonicalPaths[artifactKey] = path
		}
		entryType, _ := classifyAuditEntrypoint(path)
		extension := strings.ToLower(filepath.Ext(path))
		mediaType := auditImageMediaTypes[extension]
		signals := make([]ModAuditSignal, 0, 5)
		if duplicatePath {
			signals = append(signals, ModAuditSignal{Severity: "high", Category: "archive", Code: "duplicate_archive_path", Path: canonicalPath, Detail: "Multiple archive entries resolve to the same path", Evidence: "Duplicate normalized archive path"})
		}
		if !safeAuditArchivePath(path) {
			signals = append(signals, ModAuditSignal{Severity: "critical", Category: "archive", Code: "unsafe_archive_path", Path: canonicalPath, Detail: "Archive entry escapes its extraction root", Evidence: "Absolute or parent-relative archive path"})
		}
		if file.UncompressedSize64 > 256<<20 || file.CompressedSize64 > 0 && file.UncompressedSize64/file.CompressedSize64 > 150 {
			signals = append(signals, ModAuditSignal{Severity: "high", Category: "archive", Code: "decompression_bomb", Path: canonicalPath, Detail: "Entry has an extreme expanded size or compression ratio", Evidence: fmt.Sprintf("%d compressed bytes → %d expanded bytes", file.CompressedSize64, file.UncompressedSize64)})
		}
		if auditExecutableExtensions[extension] {
			report.ExecutableFiles++
			signals = append(signals, ModAuditSignal{Severity: "critical", Category: "executable", Code: "host_executable", Path: canonicalPath, Detail: "Archive contains a host-executable payload", Evidence: "Executable extension " + extension})
		}

		textCandidate := auditTextExtensions[extension] || entryType != "" || auditExecutableExtensions[extension]
		var excerpt string
		if inspected < maxAuditTotalRead {
			remaining := maxAuditTotalRead - inspected
			limit := int64(4 << 10)
			if textCandidate {
				limit = maxAuditEntryRead
			}
			if remaining < limit {
				limit = remaining
			}
			data, readErr := readAuditZipPrefix(file, limit)
			if readErr != nil {
				signals = append(signals, ModAuditSignal{Severity: "medium", Category: "archive", Code: "entry_read_failed", Path: canonicalPath, Detail: "Could not inspect this candidate file", Evidence: trimAuditString(readErr.Error(), 240)})
			} else {
				inspected += int64(len(data))
				if looksLikePE(data) && !auditExecutableExtensions[extension] {
					report.ExecutableFiles++
					signals = append(signals, ModAuditSignal{Severity: "critical", Category: "executable", Code: "disguised_pe", Path: canonicalPath, Detail: "File content has a Windows executable header despite its extension", Evidence: "MZ/PE signature"})
				}
				if mediaType != "" && !auditImageMatches(mediaType, data) {
					signals = append(signals, ModAuditSignal{Severity: "medium", Category: "archive", Code: "image_type_mismatch", Path: canonicalPath, Detail: "File uses a supported image extension but does not have the corresponding image signature", Evidence: "Expected " + mediaType + " content"})
				}
				if textCandidate && isProbablyAuditText(data) {
					excerpt = sanitizeAuditExcerpt(data)
					signals = append(signals, scanAuditText(canonicalPath, excerpt)...)
				}
			}
		} else {
			report.Truncated = true
		}
		signals = append(signals, scanAuditContentSignals(canonicalPath, strings.ToLower(path))...)
		signals = dedupeAuditSignals(signals)
		report.Signals = append(report.Signals, signals...)
		if entryType != "" || mediaType != "" || len(signals) > 0 {
			record := modAuditArtifactRecord{ModAuditFileArtifact: ModAuditFileArtifact{
				Path: canonicalPath, Fingerprint: fmt.Sprintf("%08x:%d", file.CRC32, file.UncompressedSize64), SizeBytes: int64(file.UncompressedSize64), EntrypointType: entryType, MediaType: mediaType, Signals: signals,
			}, Excerpt: excerpt}
			if index, exists := artifactIndexes[artifactKey]; exists {
				existing := &artifacts[index]
				existingPriority := auditArtifactPriority(*existing)
				recordPriority := auditArtifactPriority(record)
				existing.Signals = dedupeAuditSignals(append(existing.Signals, record.Signals...))
				if existing.EntrypointType == "" {
					existing.EntrypointType = record.EntrypointType
				}
				if existing.MediaType == "" {
					existing.MediaType = record.MediaType
				}
				if recordPriority > existingPriority && record.Excerpt != "" {
					existing.Fingerprint = record.Fingerprint
					existing.SizeBytes = record.SizeBytes
					existing.Excerpt = record.Excerpt
				} else if existing.Excerpt == "" {
					existing.Excerpt = record.Excerpt
				}
			} else {
				artifactIndexes[artifactKey] = len(artifacts)
				artifacts = append(artifacts, record)
			}
		}
	}
	report.BytesInspected = inspected
	sort.SliceStable(artifacts, func(left, right int) bool {
		leftScore := auditArtifactPriority(artifacts[left])
		rightScore := auditArtifactPriority(artifacts[right])
		if leftScore == rightScore {
			return artifacts[left].Path < artifacts[right].Path
		}
		return leftScore > rightScore
	})
	if len(artifacts) > maxAuditArtifacts {
		artifacts = artifacts[:maxAuditArtifacts]
		report.Truncated = true
	}
	report.Signals = dedupeAuditSignals(report.Signals)
	report.CandidateFiles = len(artifacts)
	suspicious := map[string]bool{}
	for _, signal := range report.Signals {
		if auditSeverityRank(signal.Severity) >= auditSeverityRank("medium") {
			suspicious[signal.Path] = true
		}
		if strings.HasPrefix(signal.Category, "content_") {
			report.ContentSignals++
		}
	}
	report.SuspiciousFiles = len(suspicious)
	sort.SliceStable(report.Signals, func(left, right int) bool {
		leftRank, rightRank := auditSeverityRank(report.Signals[left].Severity), auditSeverityRank(report.Signals[right].Severity)
		if leftRank == rightRank {
			if report.Signals[left].Path == report.Signals[right].Path {
				return report.Signals[left].Code < report.Signals[right].Code
			}
			return report.Signals[left].Path < report.Signals[right].Path
		}
		return leftRank > rightRank
	})
	return report, buildModAuditAttackSurface(artifacts, baseline), artifacts, nil
}

func buildModAuditAttackSurface(artifacts []modAuditArtifactRecord, baseline auditBaseline) ModAuditAttackSurface {
	surface := ModAuditAttackSurface{LibraryMods: baseline.mods, Patterns: baseline.patterns, Entries: []ModAuditAttackSurfaceEntry{}}
	for _, artifact := range artifacts {
		if artifact.EntrypointType == "" {
			continue
		}
		_, reason := classifyAuditEntrypoint(artifact.Path)
		occurrences := baseline.patterns[artifact.EntrypointType]
		surface.Entries = append(surface.Entries, ModAuditAttackSurfaceEntry{
			Path: artifact.Path, Type: artifact.EntrypointType, Reason: reason,
			LibraryOccurrences: occurrences, LibraryMods: baseline.mods, Novel: occurrences == 0,
			Signals: artifact.Signals,
		})
		if len(surface.Entries) >= maxAuditSurfaceRows {
			break
		}
	}
	return surface
}

func classifyAuditEntrypoint(path string) (string, string) {
	lower := strings.ToLower(normalizeAuditPath(path))
	extension := strings.ToLower(filepath.Ext(lower))
	switch {
	case auditExecutableExtensions[extension]:
		return "host-executable", "Host-executable payload"
	case strings.HasPrefix(lower, "lua/ge/extensions/") && extension == ".lua":
		return "game-extension", "BeamNG game-engine extension loaded by path convention"
	case strings.HasPrefix(lower, "lua/vehicle/controller/") && extension == ".lua":
		return "vehicle-controller", "Vehicle controller code"
	case strings.HasPrefix(lower, "lua/") && extension == ".lua":
		return "lua-runtime", "Lua code reachable from BeamNG runtime"
	case strings.HasPrefix(lower, "ui/modules/apps/") && (extension == ".js" || extension == ".html"):
		return "ui-app", "Legacy UI app entrypoint"
	case strings.HasPrefix(lower, "ui/ui-vue/") && (extension == ".js" || extension == ".ts"):
		return "ui-vue", "Vue UI runtime entrypoint"
	case strings.Contains(lower, "/flowgraphs/") || strings.HasSuffix(lower, ".flow.json"):
		return "flowgraph", "Data-driven flowgraph can invoke game nodes"
	case strings.Contains(lower, "/scenarios/") && (extension == ".json" || extension == ".lua"):
		return "scenario", "Scenario definition or script"
	case extension == ".js" || extension == ".ts" || extension == ".py" || extension == ".sh":
		return "script-file", "Executable script source"
	default:
		return "", ""
	}
}

func scanAuditText(path, text string) []ModAuditSignal {
	lower := strings.ToLower(text)
	signals := []ModAuditSignal{}
	for _, pattern := range auditTextPatterns {
		for _, needle := range pattern.needles {
			if strings.Contains(lower, needle) {
				signals = append(signals, ModAuditSignal{Severity: pattern.severity, Category: pattern.category, Code: pattern.code, Path: path, Detail: pattern.detail, Evidence: "Matched " + needle})
				break
			}
		}
	}
	if auditLongEncodedText.MatchString(text) {
		signals = append(signals, ModAuditSignal{Severity: "medium", Category: "obfuscation", Code: "long_encoded_blob", Path: path, Detail: "Contains a long encoded-looking string", Evidence: "Base64-like sequence longer than 240 characters"})
	}
	return append(signals, scanAuditContentSignals(path, lower)...)
}

func scanAuditContentSignals(path, lower string) []ModAuditSignal {
	type contentPattern struct {
		category string
		code     string
		detail   string
		needles  []string
	}
	patterns := []contentPattern{
		{category: "content_hateful", code: "hateful_content_signal", detail: "Contains language associated with hateful or extremist content", needles: []string{"white supremacy", "neo-nazi", "racial extermination", "holocaust denial"}},
		{category: "content_illegal", code: "illegal_content_signal", detail: "Contains language associated with illegal exploitation or criminal material", needles: []string{"child sexual abuse", "stolen credit card", "ransomware payment", "malware payload"}},
		{category: "content_graphic", code: "graphic_content_signal", detail: "Contains language associated with graphic violent content", needles: []string{"graphic gore", "decapitation", "dismemberment", "human remains"}},
	}
	result := []ModAuditSignal{}
	for _, pattern := range patterns {
		for _, needle := range pattern.needles {
			if strings.Contains(lower, needle) {
				result = append(result, ModAuditSignal{Severity: "medium", Category: pattern.category, Code: pattern.code, Path: path, Detail: pattern.detail, Evidence: "Matched content indicator " + needle})
				break
			}
		}
	}
	return result
}

func readAuditZipPrefix(file *zip.File, limit int64) ([]byte, error) {
	reader, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(io.LimitReader(reader, limit))
}

func looksLikePE(data []byte) bool {
	// MZ is the executable container signature. A malformed or truncated PE is
	// still a host-executable payload worth surfacing rather than trusting.
	return len(data) >= 2 && data[0] == 'M' && data[1] == 'Z'
}

func auditImageMatches(mediaType string, data []byte) bool {
	switch mediaType {
	case "image/png":
		return bytes.HasPrefix(data, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	case "image/jpeg":
		return len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff
	case "image/gif":
		return bytes.HasPrefix(data, []byte("GIF87a")) || bytes.HasPrefix(data, []byte("GIF89a"))
	case "image/webp":
		return len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP"))
	default:
		return false
	}
}

func isProbablyAuditText(data []byte) bool {
	if len(data) == 0 {
		return true
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return false
	}
	control := 0
	for _, value := range data {
		if value < 0x09 || value > 0x0d && value < 0x20 {
			control++
		}
	}
	return control*50 < len(data)
}

func sanitizeAuditExcerpt(data []byte) string {
	return sanitizeAuditExcerptLimit(data, maxAuditExcerpt)
}

func sanitizeAuditExcerptLimit(data []byte, limit int) string {
	text := strings.ToValidUTF8(string(data), "�")
	text = strings.ReplaceAll(text, "\x00", "")
	return trimAuditString(text, limit)
}

func normalizeAuditPath(path string) string {
	return strings.TrimPrefix(strings.ReplaceAll(strings.TrimSpace(path), "\\", "/"), "./")
}

func safeAuditArchivePath(path string) bool {
	path = normalizeAuditPath(path)
	if path == "" || strings.HasPrefix(path, "/") || len(path) >= 2 && path[1] == ':' {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if part == ".." {
			return false
		}
	}
	return true
}

func auditArtifactPriority(artifact modAuditArtifactRecord) int {
	score := 0
	if artifact.EntrypointType != "" {
		score += 20
	}
	if artifact.MediaType != "" {
		score++
	}
	for _, signal := range artifact.Signals {
		score += auditSeverityRank(signal.Severity) * 10
	}
	return score
}

func auditSeverityRank(severity string) int {
	switch strings.ToLower(severity) {
	case "critical":
		return 5
	case "high":
		return 4
	case "medium", "warning":
		return 3
	case "low":
		return 2
	default:
		return 1
	}
}

func dedupeAuditSignals(signals []ModAuditSignal) []ModAuditSignal {
	seen := map[string]bool{}
	result := make([]ModAuditSignal, 0, len(signals))
	for _, signal := range signals {
		key := signal.Path + "\x00" + signal.Code
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, signal)
	}
	return result
}

func trimAuditString(value string, limit int) string {
	value = strings.TrimSpace(strings.ToValidUTF8(value, "�"))
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	return auditStringPrefix(value, limit) + "…"
}

func auditStringPrefix(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	end := limit
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end]
}

func (store *Store) createModAudit(ctx context.Context, audit ModAudit, artifacts []modAuditArtifactRecord) error {
	deterministic, err := json.Marshal(audit.Deterministic)
	if err != nil {
		return err
	}
	surface, err := json.Marshal(audit.AttackSurface)
	if err != nil {
		return err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO mod_audits(id,entity_id,artifact_id,status,stage,created_at,updated_at,deterministic_json,attack_surface_json) VALUES(?,?,?,?,?,?,?,?,?)`, audit.ID, audit.EntityID, audit.ArtifactID, audit.Status, audit.Stage, audit.CreatedAt, audit.UpdatedAt, string(deterministic), string(surface))
	if err != nil {
		return err
	}
	for _, artifact := range artifacts {
		signals, marshalErr := json.Marshal(artifact.Signals)
		if marshalErr != nil {
			return marshalErr
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO mod_audit_files(audit_id,path,fingerprint,size_bytes,entrypoint_type,signals_json,excerpt) VALUES(?,?,?,?,?,?,?)`, audit.ID, artifact.Path, artifact.Fingerprint, artifact.SizeBytes, artifact.EntrypointType, string(signals), artifact.Excerpt)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (store *Store) latestModAudit(ctx context.Context, entityID, artifactID string) (ModAudit, error) {
	var id string
	err := store.db.QueryRowContext(ctx, `SELECT id FROM mod_audits WHERE entity_id=? AND artifact_id=? ORDER BY updated_at DESC LIMIT 1`, entityID, artifactID).Scan(&id)
	if err != nil {
		return ModAudit{}, err
	}
	return store.modAuditByID(ctx, id)
}

func (store *Store) modAuditByID(ctx context.Context, auditID string) (ModAudit, error) {
	var audit ModAudit
	var deterministic, surface, preScan, finalReport, followUps string
	err := store.db.QueryRowContext(ctx, `SELECT id,entity_id,artifact_id,status,stage,created_at,updated_at,deterministic_json,attack_surface_json,pre_scan_json,final_json,follow_up_json,error FROM mod_audits WHERE id=?`, auditID).
		Scan(&audit.ID, &audit.EntityID, &audit.ArtifactID, &audit.Status, &audit.Stage, &audit.CreatedAt, &audit.UpdatedAt, &deterministic, &surface, &preScan, &finalReport, &followUps, &audit.Error)
	if err != nil {
		return ModAudit{}, err
	}
	for _, target := range []struct {
		encoded string
		value   any
	}{
		{deterministic, &audit.Deterministic}, {surface, &audit.AttackSurface}, {preScan, &audit.PreScan}, {finalReport, &audit.Final}, {followUps, &audit.FollowUps},
	} {
		if strings.TrimSpace(target.encoded) != "" && target.encoded != "{}" {
			if err := json.Unmarshal([]byte(target.encoded), target.value); err != nil {
				return ModAudit{}, fmt.Errorf("decode Mod Audit record: %w", err)
			}
		}
	}
	if audit.FollowUps == nil {
		audit.FollowUps = []ModAuditFollowUp{}
	}
	rows, err := store.db.QueryContext(ctx, `SELECT path,fingerprint,size_bytes,entrypoint_type,signals_json,pre_scan_json FROM mod_audit_files WHERE audit_id=? ORDER BY path COLLATE NOCASE`, auditID)
	if err != nil {
		return ModAudit{}, err
	}
	defer rows.Close()
	audit.Files = []ModAuditFileArtifact{}
	for rows.Next() {
		var artifact ModAuditFileArtifact
		var signals, analysis string
		if err := rows.Scan(&artifact.Path, &artifact.Fingerprint, &artifact.SizeBytes, &artifact.EntrypointType, &signals, &analysis); err != nil {
			return ModAudit{}, err
		}
		artifact.MediaType = auditImageMediaTypes[strings.ToLower(filepath.Ext(artifact.Path))]
		if err := json.Unmarshal([]byte(signals), &artifact.Signals); err != nil {
			return ModAudit{}, err
		}
		if analysis != "" && analysis != "{}" {
			var parsed ModAuditPreScanFile
			if err := json.Unmarshal([]byte(analysis), &parsed); err != nil {
				return ModAudit{}, err
			}
			artifact.PreScan = &parsed
		}
		audit.Files = append(audit.Files, artifact)
	}
	return audit, rows.Err()
}

func (store *Store) modAuditArtifactRecords(ctx context.Context, auditID string) ([]modAuditArtifactRecord, error) {
	rows, err := store.db.QueryContext(ctx, `SELECT path,fingerprint,size_bytes,entrypoint_type,signals_json,excerpt,pre_scan_json FROM mod_audit_files WHERE audit_id=? ORDER BY path COLLATE NOCASE`, auditID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []modAuditArtifactRecord{}
	for rows.Next() {
		var record modAuditArtifactRecord
		var signals, analysis string
		if err := rows.Scan(&record.Path, &record.Fingerprint, &record.SizeBytes, &record.EntrypointType, &signals, &record.Excerpt, &analysis); err != nil {
			return nil, err
		}
		record.MediaType = auditImageMediaTypes[strings.ToLower(filepath.Ext(record.Path))]
		if err := json.Unmarshal([]byte(signals), &record.Signals); err != nil {
			return nil, err
		}
		if analysis != "" && analysis != "{}" {
			var parsed ModAuditPreScanFile
			if err := json.Unmarshal([]byte(analysis), &parsed); err != nil {
				return nil, err
			}
			record.PreScan = &parsed
		}
		result = append(result, record)
	}
	return result, rows.Err()
}

func (store *Store) updateModAuditStatus(ctx context.Context, auditID, status, stage, auditError string) error {
	_, err := store.db.ExecContext(ctx, `UPDATE mod_audits SET status=?,stage=?,updated_at=?,error=? WHERE id=?`, status, stage, nowUTC(), trimAuditString(auditError, 4000), auditID)
	return err
}

func (store *Store) saveModAuditPreScan(ctx context.Context, auditID string, report ModAuditPreScanReport) error {
	encoded, err := json.Marshal(report)
	if err != nil {
		return err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE mod_audits SET status='pre_scan_complete',stage='pre_scan',updated_at=?,pre_scan_json=?,error='' WHERE id=?`, nowUTC(), string(encoded), auditID); err != nil {
		return err
	}
	for _, analysis := range report.Files {
		fileJSON, marshalErr := json.Marshal(analysis)
		if marshalErr != nil {
			return marshalErr
		}
		if _, err := tx.ExecContext(ctx, `UPDATE mod_audit_files SET pre_scan_json=? WHERE audit_id=? AND path=?`, string(fileJSON), auditID, analysis.Path); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (store *Store) saveModAuditFinal(ctx context.Context, auditID string, report ModAuditFinalReport) error {
	encoded, err := json.Marshal(report)
	if err != nil {
		return err
	}
	_, err = store.db.ExecContext(ctx, `UPDATE mod_audits SET status='complete',stage='full',updated_at=?,final_json=?,error='' WHERE id=?`, nowUTC(), string(encoded), auditID)
	return err
}

func (store *Store) appendModAuditFollowUp(ctx context.Context, auditID string, followUp ModAuditFollowUp) error {
	audit, err := store.modAuditByID(ctx, auditID)
	if err != nil {
		return err
	}
	audit.FollowUps = append(audit.FollowUps, followUp)
	encoded, err := json.Marshal(audit.FollowUps)
	if err != nil {
		return err
	}
	_, err = store.db.ExecContext(ctx, `UPDATE mod_audits SET updated_at=?,follow_up_json=?,error='' WHERE id=?`, nowUTC(), string(encoded), auditID)
	return err
}
