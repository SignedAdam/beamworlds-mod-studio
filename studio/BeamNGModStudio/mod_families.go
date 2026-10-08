package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
)

type ModFamilyMember struct {
	EntityID        string   `json:"entityId"`
	LinkID          string   `json:"linkId"`
	DisplayName     string   `json:"displayName"`
	Title           string   `json:"title"`
	Author          string   `json:"author"`
	Version         string   `json:"version"`
	ArchivePath     string   `json:"archivePath"`
	SizeBytes       int64    `json:"sizeBytes"`
	ModifiedAt      string   `json:"modifiedAt"`
	SHA256          string   `json:"sha256"`
	SourceLabel     string   `json:"sourceLabel"`
	Collections     []string `json:"collections"`
	Tags            []string `json:"tags"`
	WorkspaceCount  int      `json:"workspaceCount"`
	ThumbnailURL    string   `json:"thumbnailUrl"`
	EntryCount      int      `json:"entryCount"`
	VariantCount    int      `json:"variantCount"`
	Namespaces      []string `json:"namespaces"`
	IssueCount      int      `json:"issueCount"`
	IssueSeverity   string   `json:"issueSeverity"`
	HealthStatus    string   `json:"healthStatus"`
	InstalledInGame bool     `json:"installedInGame"`
	Keeper          bool     `json:"keeper"`
	KeeperReason    string   `json:"keeperReason"`
}

type ModFamily struct {
	ID               string            `json:"id"`
	Confidence       string            `json:"confidence"`
	Kind             string            `json:"kind"`
	Title            string            `json:"title"`
	Author           string            `json:"author"`
	ResourceID       string            `json:"resourceId"`
	Members          []ModFamilyMember `json:"members"`
	ReclaimableBytes int64             `json:"reclaimableBytes"`
}

type modFamilyManifest struct {
	Title             string                      `json:"title"`
	Author            string                      `json:"author"`
	Version           string                      `json:"version"`
	Filename          string                      `json:"filename"`
	Namespaces        map[string][]string         `json:"namespaces"`
	Members           []json.RawMessage           `json:"members"`
	Variants          []json.RawMessage           `json:"variants"`
	Issues            []modFamilyIssue            `json:"issues"`
	MetadataDocuments []modFamilyMetadataDocument `json:"metadataDocuments"`
}

type modFamilyIssue struct {
	Severity string `json:"severity"`
}

type modFamilyMetadataDocument struct {
	Data map[string]json.RawMessage `json:"data"`
}

type modFamilyArchiveLink struct {
	LinkID          string
	ArtifactID      string
	ArchivePath     string
	SizeBytes       int64
	ModifiedAt      string
	SHA256          string
	Fingerprint     string
	SourceLabel     string
	Manifest        modFamilyManifest
	InstalledInGame bool
}

type modFamilyCandidate struct {
	EntityID        string
	LinkID          string
	ArtifactID      string
	DisplayName     string
	Title           string
	Author          string
	Version         string
	ArchivePath     string
	SizeBytes       int64
	ModifiedAt      string
	SHA256          string
	Fingerprint     string
	SourceLabel     string
	ThumbnailURL    string
	Manifest        modFamilyManifest
	HealthStatus    string
	InstalledInGame bool
	resourceIDs     []string
	archiveLinks    []modFamilyArchiveLink
}

const modFamilyRowsQuery = `SELECT l.entity_id, l.id, COALESCE(l.artifact_id,''),
	e.display_name,
	CASE lower(COALESCE(NULLIF(l.source_id,''),NULLIF(e.source_id,''),'user-added'))
		WHEN 'beamng-repository' THEN 'BeamNG Repository'
		WHEN 'user-added' THEN 'User added'
		ELSE COALESCE(sc.label,'')
	END,
	COALESCE(l.path,''), COALESCE(l.size_bytes,0), COALESCE(l.modified_at,''),
	COALESCE(a.sha256,''), COALESCE(a.central_fingerprint,''), COALESCE(a.manifest_json,'{}'), COALESCE(ast.sha256,'')
FROM archive_links l
JOIN entities e ON e.id=l.entity_id
LEFT JOIN source_classifications sc ON sc.id=COALESCE(NULLIF(l.source_id,''),NULLIF(e.source_id,''),'user-added')
LEFT JOIN artifacts a ON a.id=l.artifact_id
LEFT JOIN entity_assets ea ON ea.entity_id=e.id AND ea.role='thumbnail' AND ea.ordinal=0
LEFT JOIN assets ast ON ast.sha256=ea.asset_sha256
WHERE l.active=1 AND COALESCE(e.archived_at,'') = ''
ORDER BY l.entity_id, l.active DESC, l.last_seen_at DESC, l.id DESC`

// ModFamilies returns every currently visible family. The computation is kept
// in the store so the service method remains the same thin Wails boundary as
// the other library APIs.
func (service *AppService) ModFamilies() ([]ModFamily, error) {
	return service.store.modFamilies(context.Background(), service.config.ActiveModsDir)
}

// DismissModFamily records the current membership signature for one family.
// The raw family set is used for lookup so dismissing an already dismissed
// family remains idempotent.
func (service *AppService) DismissModFamily(familyID string) ([]ModFamily, error) {
	familyID = strings.TrimSpace(familyID)
	if familyID == "" {
		return nil, errors.New("family ID is required")
	}
	ctx := context.Background()
	families, err := service.store.buildModFamilies(ctx, service.config.ActiveModsDir)
	if err != nil {
		return nil, err
	}
	var family *ModFamily
	for index := range families {
		if families[index].ID == familyID {
			family = &families[index]
			break
		}
	}
	if family == nil {
		return nil, fmt.Errorf("mod family %q was not found", familyID)
	}
	if err := service.store.writeSetting(ctx, modFamilyDismissalKey(familyID), modFamilyMembershipSignature(*family)); err != nil {
		return nil, err
	}
	return service.store.modFamilies(ctx, service.config.ActiveModsDir)
}

const modFamilyDismissedPrefix = "mod_family_dismissed:"

func modFamilyDismissalKey(familyID string) string {
	return modFamilyDismissedPrefix + familyID
}

func modFamilyMembershipSignature(family ModFamily) string {
	values := make([]string, 0, len(family.Members))
	for _, member := range family.Members {
		if strings.HasPrefix(family.ID, "files:") {
			values = append(values, member.LinkID)
		} else {
			values = append(values, member.EntityID)
		}
	}
	sort.Strings(values)
	digest := sha256.Sum256([]byte(strings.Join(values, "\x00")))
	return hex.EncodeToString(digest[:])
}

func (s *Store) modFamilies(ctx context.Context, activeModsDir string) ([]ModFamily, error) {
	families, err := s.buildModFamilies(ctx, activeModsDir)
	if err != nil {
		return nil, err
	}
	visible := make([]ModFamily, 0, len(families))
	for _, family := range families {
		stored, err := s.readSetting(ctx, modFamilyDismissalKey(family.ID))
		if errors.Is(err, sql.ErrNoRows) {
			visible = append(visible, family)
			continue
		}
		if err != nil {
			return nil, err
		}
		if stored != modFamilyMembershipSignature(family) {
			visible = append(visible, family)
		}
	}
	return visible, nil
}

func (s *Store) buildModFamilies(ctx context.Context, activeModsDir string) ([]ModFamily, error) {
	candidates, err := s.modFamilyCandidates(ctx, activeModsDir)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return []ModFamily{}, nil
	}

	families := make([]ModFamily, 0, len(candidates))
	memberCache := make(map[string]ModFamilyMember)
	for _, candidate := range candidates {
		if len(candidate.archiveLinks) < 2 {
			continue
		}
		family, err := s.makeFileModFamily(ctx, candidate, memberCache)
		if err != nil {
			return nil, err
		}
		families = append(families, family)
	}

	repoGroups := make(map[string][]*modFamilyCandidate)
	for _, candidate := range candidates {
		seen := make(map[string]struct{}, len(candidate.resourceIDs))
		for _, resourceID := range candidate.resourceIDs {
			if _, exists := seen[resourceID]; exists {
				continue
			}
			seen[resourceID] = struct{}{}
			repoGroups[resourceID] = append(repoGroups[resourceID], candidate)
		}
	}
	repoIDs := make([]string, 0, len(repoGroups))
	for resourceID := range repoGroups {
		repoIDs = append(repoIDs, resourceID)
	}
	sort.Strings(repoIDs)

	placedInRepo := make(map[string]struct{})
	for _, resourceID := range repoIDs {
		available := make([]*modFamilyCandidate, 0, len(repoGroups[resourceID]))
		for _, candidate := range repoGroups[resourceID] {
			if _, placed := placedInRepo[candidate.EntityID]; placed {
				continue
			}
			available = append(available, candidate)
		}
		members := uniqueModFamilyCandidates(available)
		if len(members) < 2 {
			continue
		}
		family, err := s.makeModFamily(ctx, "repo:"+resourceID, "repo", resourceID, members, memberCache)
		if err != nil {
			return nil, err
		}
		families = append(families, family)
		for _, member := range members {
			placedInRepo[member.EntityID] = struct{}{}
		}
	}

	contentGroups := make(map[string][]*modFamilyCandidate)
	for _, candidate := range candidates {
		if _, placed := placedInRepo[candidate.EntityID]; placed {
			continue
		}
		title := normalizeModFamilyText(candidate.Title)
		namespaces := modFamilyNamespaces(candidate.Manifest)
		if title == "" || len(namespaces) == 0 {
			continue
		}
		key := modFamilyContentKey(title, namespaces)
		contentGroups[key] = append(contentGroups[key], candidate)
	}
	contentKeys := make([]string, 0, len(contentGroups))
	for key := range contentGroups {
		contentKeys = append(contentKeys, key)
	}
	sort.Strings(contentKeys)

	placedInContent := make(map[string]struct{})
	for _, key := range contentKeys {
		members := uniqueModFamilyCandidates(contentGroups[key])
		if len(members) < 2 {
			continue
		}
		digest := sha256.Sum256([]byte(key))
		familyID := "content:" + hex.EncodeToString(digest[:])[:16]
		family, err := s.makeModFamily(ctx, familyID, "content", "", members, memberCache)
		if err != nil {
			return nil, err
		}
		families = append(families, family)
		for _, member := range members {
			placedInContent[member.EntityID] = struct{}{}
		}
	}

	metadataGroups := make(map[string][]*modFamilyCandidate)
	for _, candidate := range candidates {
		if _, placed := placedInRepo[candidate.EntityID]; placed {
			continue
		}
		if _, placed := placedInContent[candidate.EntityID]; placed {
			continue
		}
		title := normalizeModFamilyText(candidate.Title)
		author := normalizeModFamilyText(candidate.Author)
		if title == "" || author == "" {
			continue
		}
		key := title + "\x00" + author
		metadataGroups[key] = append(metadataGroups[key], candidate)
	}
	metadataKeys := make([]string, 0, len(metadataGroups))
	for key := range metadataGroups {
		metadataKeys = append(metadataKeys, key)
	}
	sort.Strings(metadataKeys)
	for _, key := range metadataKeys {
		members := uniqueModFamilyCandidates(metadataGroups[key])
		if len(members) < 2 {
			continue
		}
		digest := sha256.Sum256([]byte(key))
		familyID := "meta:" + hex.EncodeToString(digest[:])[:16]
		family, err := s.makeModFamily(ctx, familyID, "metadata", "", members, memberCache)
		if err != nil {
			return nil, err
		}
		families = append(families, family)
	}

	familyRank := func(confidence string) int {
		switch confidence {
		case "identical":
			return 0
		case "repo":
			return 1
		case "content":
			return 2
		case "metadata":
			return 3
		default:
			return 4
		}
	}
	sort.SliceStable(families, func(left, right int) bool {
		if leftRank, rightRank := familyRank(families[left].Confidence), familyRank(families[right].Confidence); leftRank != rightRank {
			return leftRank < rightRank
		}
		if len(families[left].Members) != len(families[right].Members) {
			return len(families[left].Members) > len(families[right].Members)
		}
		leftTitle := normalizeModFamilyText(families[left].Title)
		rightTitle := normalizeModFamilyText(families[right].Title)
		if leftTitle != rightTitle {
			return leftTitle < rightTitle
		}
		if families[left].Title != families[right].Title {
			return families[left].Title < families[right].Title
		}
		return families[left].ID < families[right].ID
	})
	return families, nil
}

func (s *Store) modFamilyCandidates(ctx context.Context, activeModsDir string) ([]*modFamilyCandidate, error) {
	rows, err := s.db.QueryContext(ctx, modFamilyRowsQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	candidates := make([]*modFamilyCandidate, 0)
	byEntity := make(map[string]*modFamilyCandidate)
	for rows.Next() {
		var entityID, linkID, artifactID, displayName, sourceLabel, archivePath, modifiedAt, sha256Value, fingerprint, manifestJSON, assetSHA string
		var sizeBytes int64
		if err := rows.Scan(&entityID, &linkID, &artifactID, &displayName, &sourceLabel, &archivePath, &sizeBytes, &modifiedAt, &sha256Value, &fingerprint, &manifestJSON, &assetSHA); err != nil {
			return nil, err
		}
		var manifest modFamilyManifest
		if err := json.Unmarshal([]byte(manifestJSON), &manifest); err != nil {
			return nil, fmt.Errorf("decode manifest for mod family entity %q: %w", entityID, err)
		}
		link := modFamilyArchiveLink{
			LinkID:      linkID,
			ArtifactID:  artifactID,
			ArchivePath: archivePath,
			SizeBytes:   sizeBytes,
			ModifiedAt:  modifiedAt,
			SHA256:      sha256Value,
			Fingerprint: fingerprint,
			SourceLabel: sourceLabel,
			Manifest:    manifest,
		}
		candidate, exists := byEntity[entityID]
		if !exists {
			candidate = &modFamilyCandidate{
				EntityID:     entityID,
				LinkID:       linkID,
				ArtifactID:   artifactID,
				DisplayName:  displayName,
				Title:        manifest.Title,
				Author:       manifest.Author,
				Version:      manifest.Version,
				ArchivePath:  archivePath,
				SizeBytes:    sizeBytes,
				ModifiedAt:   modifiedAt,
				SHA256:       sha256Value,
				Fingerprint:  fingerprint,
				SourceLabel:  sourceLabel,
				ThumbnailURL: modFamilyThumbnailURL(assetSHA),
				Manifest:     manifest,
				resourceIDs:  []string{},
				archiveLinks: []modFamilyArchiveLink{},
			}
			candidates = append(candidates, candidate)
			byEntity[entityID] = candidate
		}
		candidate.archiveLinks = append(candidate.archiveLinks, link)
		for _, resourceID := range modFamilyResourceIDs(manifest) {
			if !containsString(candidate.resourceIDs, resourceID) {
				candidate.resourceIDs = append(candidate.resourceIDs, resourceID)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	healthStatuses, err := s.modFamilyHealthStatuses(ctx, candidates)
	if err != nil {
		return nil, err
	}
	enabledPaths, enabledFilenames := modFamilyInstalledKeys(activeModsDir)
	for _, candidate := range candidates {
		candidate.HealthStatus = healthStatuses[candidate.EntityID]
		candidate.InstalledInGame = modFamilyArchiveInstalled(candidate.ArchivePath, enabledPaths, enabledFilenames)
		for index := range candidate.archiveLinks {
			candidate.archiveLinks[index].InstalledInGame = modFamilyArchiveInstalled(candidate.archiveLinks[index].ArchivePath, enabledPaths, enabledFilenames)
		}
	}
	return candidates, nil
}

// modFamilyHealthStatuses mirrors attachLibraryItemHealthQuery's current-scan
// selection and status precedence. Keep both implementations in sync.
func (s *Store) modFamilyHealthStatuses(ctx context.Context, candidates []*modFamilyCandidate) (map[string]string, error) {
	statuses := make(map[string]string, len(candidates))
	for _, candidate := range candidates {
		status := "unscanned"
		if modFamilyManifestBroken(candidate.Manifest) {
			status = "broken"
		}
		statuses[candidate.EntityID] = status
	}
	if len(candidates) == 0 {
		return statuses, nil
	}

	type healthRecord struct {
		status, verdict string
	}
	currentHealth := map[string]healthRecord{}
	rows, err := s.db.QueryContext(ctx, `SELECT entity_id,artifact_id,status,verdict,current_ordinal FROM (
		SELECT v.entity_id,v.artifact_id,v.status,v.verdict,
			ROW_NUMBER() OVER(PARTITION BY v.entity_id,v.artifact_id ORDER BY v.updated_at DESC,v.id DESC) AS current_ordinal
		FROM virus_scans v
	) WHERE current_ordinal=1`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var entityID, artifactID string
		var record healthRecord
		var currentOrdinal int
		if err := rows.Scan(&entityID, &artifactID, &record.status, &record.verdict, &currentOrdinal); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if currentOrdinal == 1 {
			currentHealth[entityID+"\x00"+artifactID] = record
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for _, candidate := range candidates {
		record, ok := currentHealth[candidate.EntityID+"\x00"+candidate.ArtifactID]
		if !ok {
			continue
		}
		status := record.verdict
		if record.status == "running" {
			status = "scanning"
		} else if record.status == "failed" {
			status = "scan_failed"
		}
		if status == "" {
			status = "unscanned"
		}
		if status != "threat" && modFamilyManifestBroken(candidate.Manifest) {
			status = "broken"
		}
		statuses[candidate.EntityID] = status
	}
	return statuses, nil
}

func modFamilyInstalledKeys(activeModsDir string) (map[string]struct{}, map[string]string) {
	paths := map[string]struct{}{}
	filenames := map[string]string{}
	activeModsDir = strings.TrimSpace(activeModsDir)
	if activeModsDir == "" {
		return paths, filenames
	}
	loadedPaths, loadedFilenames, _, _, err := beamNGEnabledArchiveKeysFrom(activeModsDir, filepath.Join(activeModsDir, "db.json"))
	if err != nil {
		// An unavailable database only means that presence cannot be proven.
		return paths, filenames
	}
	return loadedPaths, loadedFilenames
}

func modFamilyArchiveInstalled(archivePath string, paths map[string]struct{}, filenames map[string]string) bool {
	archivePath = strings.TrimSpace(archivePath)
	if archivePath == "" {
		return false
	}
	if _, ok := paths[archiveSourcePathKey(archivePath)]; ok {
		return true
	}
	_, ok := filenames[archiveSourceFilenameKey(archivePath)]
	return ok
}

func modFamilyNamespaces(manifest modFamilyManifest) []string {
	unique := make(map[string]struct{})
	for _, values := range manifest.Namespaces {
		for _, value := range values {
			value = strings.TrimSpace(value)
			if value != "" {
				unique[value] = struct{}{}
			}
		}
	}
	namespaces := make([]string, 0, len(unique))
	for namespace := range unique {
		namespaces = append(namespaces, namespace)
	}
	sort.Strings(namespaces)
	return namespaces
}

func modFamilyContentKey(title string, namespaces []string) string {
	return title + "\x00" + strings.Join(namespaces, "\x00")
}

func modFamilyManifestBroken(manifest modFamilyManifest) bool {
	for _, issue := range manifest.Issues {
		if strings.EqualFold(strings.TrimSpace(issue.Severity), "error") {
			return true
		}
	}
	return false
}

func modFamilyIssueSeverity(manifest modFamilyManifest) string {
	worst := ""
	worstRank := 0
	for _, issue := range manifest.Issues {
		severity := strings.ToLower(strings.TrimSpace(issue.Severity))
		rank := 0
		switch severity {
		case "info":
			rank = 1
		case "warning":
			rank = 2
		case "error":
			rank = 3
		default:
			continue
		}
		if rank > worstRank {
			worstRank = rank
			worst = severity
		}
	}
	return worst
}

func modFamilyResourceIDs(manifest modFamilyManifest) []string {
	resourceIDs := []string{}
	for _, document := range manifest.MetadataDocuments {
		keys := make([]string, 0, len(document.Data))
		for key := range document.Data {
			if normalizeModFamilyMetadataKey(key) == "resourceid" {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		for _, key := range keys {
			resourceID, ok := canonicalModFamilyResourceID(document.Data[key])
			if !ok {
				continue
			}
			if !containsString(resourceIDs, resourceID) {
				resourceIDs = append(resourceIDs, resourceID)
			}
			break
		}
	}
	return resourceIDs
}

func normalizeModFamilyMetadataKey(value string) string {
	var builder strings.Builder
	for _, char := range value {
		if unicode.IsLetter(char) || unicode.IsDigit(char) {
			builder.WriteRune(unicode.ToLower(char))
		}
	}
	return builder.String()
}

func canonicalModFamilyResourceID(raw json.RawMessage) (string, bool) {
	var stringValue string
	if err := json.Unmarshal(raw, &stringValue); err == nil {
		return canonicalModFamilyResourceIDText(stringValue)
	}
	var numberValue json.Number
	if err := json.Unmarshal(raw, &numberValue); err != nil {
		return "", false
	}
	return canonicalModFamilyResourceIDText(numberValue.String())
}

func canonicalModFamilyResourceIDText(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	sign := ""
	if value[0] == '+' || value[0] == '-' {
		sign = value[:1]
		value = value[1:]
	}
	if value == "" {
		return "", false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return strings.TrimSpace(sign + value), true
		}
	}
	value = strings.TrimLeft(value, "0")
	if value == "" {
		value = "0"
	}
	return sign + value, true
}

func modFamilyThumbnailURL(assetSHA string) string {
	if strings.TrimSpace(assetSHA) == "" {
		return ""
	}
	return "/cache/" + assetSHA
}

func uniqueModFamilyCandidates(candidates []*modFamilyCandidate) []*modFamilyCandidate {
	result := make([]*modFamilyCandidate, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if _, exists := seen[candidate.EntityID]; exists {
			continue
		}
		seen[candidate.EntityID] = struct{}{}
		result = append(result, candidate)
	}
	return result
}

func (s *Store) makeModFamily(ctx context.Context, familyID, confidence, resourceID string, candidates []*modFamilyCandidate, cache map[string]ModFamilyMember) (ModFamily, error) {
	members := make([]ModFamilyMember, 0, len(candidates))
	for _, candidate := range candidates {
		link := modFamilyArchiveLink{
			LinkID:          candidate.LinkID,
			ArtifactID:      candidate.ArtifactID,
			ArchivePath:     candidate.ArchivePath,
			SizeBytes:       candidate.SizeBytes,
			ModifiedAt:      candidate.ModifiedAt,
			SHA256:          candidate.SHA256,
			Fingerprint:     candidate.Fingerprint,
			SourceLabel:     candidate.SourceLabel,
			Manifest:        candidate.Manifest,
			InstalledInGame: candidate.InstalledInGame,
		}
		member, err := s.makeModFamilyMember(ctx, candidate, link, cache)
		if err != nil {
			return ModFamily{}, err
		}
		members = append(members, member)
	}
	sort.SliceStable(members, func(left, right int) bool {
		return modFamilyMemberLess(members[left], members[right])
	})
	if len(members) > 0 {
		members[0].Keeper = true
		members[0].KeeperReason = modFamilyKeeperReason(members, 0)
	}
	for index := 1; index < len(members); index++ {
		members[index].Keeper = false
		members[index].KeeperReason = ""
	}

	family := ModFamily{
		ID:               familyID,
		Confidence:       confidence,
		Kind:             modFamilyKind(candidates),
		Title:            "",
		Author:           "",
		ResourceID:       resourceID,
		Members:          members,
		ReclaimableBytes: 0,
	}
	if len(members) > 0 {
		family.Title = members[0].Title
		family.Author = members[0].Author
	}
	for index, member := range members {
		if index > 0 {
			family.ReclaimableBytes += member.SizeBytes
		}
	}
	return family, nil
}

func (s *Store) makeModFamilyMember(ctx context.Context, candidate *modFamilyCandidate, link modFamilyArchiveLink, cache map[string]ModFamilyMember) (ModFamilyMember, error) {
	base, exists := cache[candidate.EntityID]
	if !exists {
		collections, err := s.scanStrings(ctx, `SELECT c.name FROM collection_mods cm JOIN collections c ON c.id=cm.collection_id WHERE cm.entity_id=? ORDER BY c.name COLLATE NOCASE,c.id`, candidate.EntityID)
		if err != nil {
			return ModFamilyMember{}, err
		}
		if collections == nil {
			collections = []string{}
		}
		tags, err := s.scanStrings(ctx, `SELECT t.name FROM mod_tag_entities mt JOIN mod_tags t ON t.id=mt.tag_id WHERE mt.entity_id=? ORDER BY t.name COLLATE NOCASE,t.id`, candidate.EntityID)
		if err != nil {
			return ModFamilyMember{}, err
		}
		if tags == nil {
			tags = []string{}
		}
		var workspaceCount int
		base = ModFamilyMember{
			EntityID:        candidate.EntityID,
			LinkID:          candidate.LinkID,
			DisplayName:     candidate.DisplayName,
			Title:           candidate.Title,
			Author:          candidate.Author,
			Version:         candidate.Version,
			ArchivePath:     candidate.ArchivePath,
			SizeBytes:       candidate.SizeBytes,
			ModifiedAt:      candidate.ModifiedAt,
			SHA256:          candidate.SHA256,
			SourceLabel:     candidate.SourceLabel,
			Collections:     collections,
			Tags:            tags,
			WorkspaceCount:  workspaceCount,
			ThumbnailURL:    candidate.ThumbnailURL,
			Namespaces:      []string{},
			HealthStatus:    candidate.HealthStatus,
			InstalledInGame: candidate.InstalledInGame,
			Keeper:          false,
			KeeperReason:    "",
		}
		cache[candidate.EntityID] = base
	}
	member := base
	member.LinkID = link.LinkID
	member.ArchivePath = link.ArchivePath
	member.SizeBytes = link.SizeBytes
	member.ModifiedAt = link.ModifiedAt
	member.SHA256 = link.SHA256
	member.SourceLabel = link.SourceLabel
	member.Collections = append([]string{}, base.Collections...)
	member.Tags = append([]string{}, base.Tags...)
	member.EntryCount = len(link.Manifest.Members)
	member.VariantCount = len(link.Manifest.Variants)
	member.Namespaces = modFamilyNamespaces(link.Manifest)
	member.IssueCount = len(link.Manifest.Issues)
	member.IssueSeverity = modFamilyIssueSeverity(link.Manifest)
	member.HealthStatus = candidate.HealthStatus
	member.InstalledInGame = link.InstalledInGame
	return member, nil
}
func (s *Store) makeFileModFamily(ctx context.Context, candidate *modFamilyCandidate, cache map[string]ModFamilyMember) (ModFamily, error) {
	members := make([]ModFamilyMember, 0, len(candidate.archiveLinks))
	for _, link := range candidate.archiveLinks {
		member, err := s.makeModFamilyMember(ctx, candidate, link, cache)
		if err != nil {
			return ModFamily{}, err
		}
		members = append(members, member)
	}
	sort.SliceStable(members, func(left, right int) bool {
		return modFamilyFileMemberLess(members[left], members[right])
	})
	if len(members) > 0 {
		members[0].Keeper = true
		members[0].KeeperReason = modFamilyFileKeeperReason(members, 0)
	}
	for index := 1; index < len(members); index++ {
		members[index].Keeper = false
		members[index].KeeperReason = ""
	}
	family := ModFamily{
		ID:               "files:" + candidate.EntityID,
		Confidence:       "identical",
		Kind:             "copies",
		Title:            candidate.Title,
		Author:           candidate.Author,
		ResourceID:       "",
		Members:          members,
		ReclaimableBytes: 0,
	}
	for index, member := range members {
		if index > 0 {
			family.ReclaimableBytes += member.SizeBytes
		}
	}
	return family, nil
}

func modFamilyFileMemberLess(left, right ModFamilyMember) bool {
	if comparison := compareModFamilyModified(left.ModifiedAt, right.ModifiedAt); comparison != 0 {
		return comparison > 0
	}
	if left.SizeBytes != right.SizeBytes {
		return left.SizeBytes > right.SizeBytes
	}
	return left.LinkID < right.LinkID
}

func modFamilyFileKeeperReason(members []ModFamilyMember, keeperIndex int) string {
	if len(members) <= 1 {
		return "only remaining copy"
	}
	keeper := members[keeperIndex]
	newestIsStrictlyBest := true
	for index, member := range members {
		if index == keeperIndex {
			continue
		}
		if compareModFamilyModified(keeper.ModifiedAt, member.ModifiedAt) <= 0 {
			newestIsStrictlyBest = false
			break
		}
	}
	if newestIsStrictlyBest {
		return "newest file"
	}
	modifiedTies := make([]int, 0, len(members))
	for index, member := range members {
		if compareModFamilyModified(keeper.ModifiedAt, member.ModifiedAt) == 0 {
			modifiedTies = append(modifiedTies, index)
		}
	}
	largestIsStrictlyBest := true
	for _, index := range modifiedTies {
		if index == keeperIndex {
			continue
		}
		if keeper.SizeBytes <= members[index].SizeBytes {
			largestIsStrictlyBest = false
			break
		}
	}
	if largestIsStrictlyBest {
		return "largest archive"
	}
	return "identical copy"
}

// modFamilyKind decides copies versus versions from the archive identity the
// scanner actually records. The full SHA-256 is populated for only a fraction
// of a real library (20 of 453 artifacts here) and the manifest never carries
// it, so requiring it would label identical copies as versions. The central
// fingerprint is the scanner's own content key and is always present; the full
// hash only corroborates when both members have one.
func modFamilyKind(candidates []*modFamilyCandidate) string {
	if len(candidates) == 0 {
		return "versions"
	}
	identical := func(get func(*modFamilyCandidate) string) bool {
		first := strings.TrimSpace(get(candidates[0]))
		if first == "" {
			return false
		}
		for _, candidate := range candidates[1:] {
			value := strings.TrimSpace(get(candidate))
			if value == "" || !strings.EqualFold(value, first) {
				return false
			}
		}
		return true
	}
	if identical(func(candidate *modFamilyCandidate) string { return candidate.Fingerprint }) {
		return "copies"
	}
	if identical(func(candidate *modFamilyCandidate) string { return candidate.SHA256 }) {
		return "copies"
	}
	return "versions"
}

func modFamilyMemberLess(left, right ModFamilyMember) bool {
	if comparison := compareModFamilyVersions(left.Version, right.Version); comparison != 0 {
		return comparison > 0
	}
	if comparison := compareModFamilyModified(left.ModifiedAt, right.ModifiedAt); comparison != 0 {
		return comparison > 0
	}
	if left.SizeBytes != right.SizeBytes {
		return left.SizeBytes > right.SizeBytes
	}
	return left.EntityID < right.EntityID
}

func modFamilyKeeperReason(members []ModFamilyMember, keeperIndex int) string {
	if len(members) <= 1 {
		return "only remaining copy"
	}
	keeper := members[keeperIndex]
	versionIsStrictlyBest := true
	for index, member := range members {
		if index == keeperIndex {
			continue
		}
		if compareModFamilyVersions(keeper.Version, member.Version) <= 0 {
			versionIsStrictlyBest = false
			break
		}
	}
	if versionIsStrictlyBest && strings.TrimSpace(keeper.Version) != "" {
		return "version " + keeper.Version
	}
	versionTies := make([]int, 0, len(members))
	for index, member := range members {
		if compareModFamilyVersions(keeper.Version, member.Version) == 0 {
			versionTies = append(versionTies, index)
		}
	}
	newestIsStrictlyBest := true
	for _, index := range versionTies {
		if index == keeperIndex {
			continue
		}
		if compareModFamilyModified(keeper.ModifiedAt, members[index].ModifiedAt) <= 0 {
			newestIsStrictlyBest = false
			break
		}
	}
	if newestIsStrictlyBest {
		return "newest file"
	}
	modifiedTies := make([]int, 0, len(versionTies))
	for _, index := range versionTies {
		if compareModFamilyModified(keeper.ModifiedAt, members[index].ModifiedAt) == 0 {
			modifiedTies = append(modifiedTies, index)
		}
	}
	largestIsStrictlyBest := true
	for _, index := range modifiedTies {
		if index == keeperIndex {
			continue
		}
		if keeper.SizeBytes <= members[index].SizeBytes {
			largestIsStrictlyBest = false
			break
		}
	}
	if largestIsStrictlyBest {
		return "largest archive"
	}
	// Version, file date, and size all tie: the copies are interchangeable and
	// the keeper was picked by ID. Saying "newest file" here would be a lie.
	return "identical copy"
}

func compareModFamilyVersions(left, right string) int {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	if left == "" && right == "" {
		return 0
	}
	if left == "" {
		return -1
	}
	if right == "" {
		return 1
	}
	leftParts := modFamilyVersionParts(left)
	rightParts := modFamilyVersionParts(right)
	length := len(leftParts)
	if len(rightParts) > length {
		length = len(rightParts)
	}
	for index := range length {
		leftPart, rightPart := "0", "0"
		if index < len(leftParts) {
			leftPart = leftParts[index]
		}
		if index < len(rightParts) {
			rightPart = rightParts[index]
		}
		if len(leftPart) != len(rightPart) {
			if len(leftPart) > len(rightPart) {
				return 1
			}
			return -1
		}
		if leftPart != rightPart {
			if leftPart > rightPart {
				return 1
			}
			return -1
		}
	}
	return 0
}

func modFamilyVersionParts(value string) []string {
	parts := make([]string, 0)
	start := -1
	for index, char := range value {
		if char >= '0' && char <= '9' {
			if start < 0 {
				start = index
			}
			continue
		}
		if start >= 0 {
			parts = append(parts, canonicalModFamilyNumber(value[start:index]))
			start = -1
		}
	}
	if start >= 0 {
		parts = append(parts, canonicalModFamilyNumber(value[start:]))
	}
	return parts
}

func canonicalModFamilyNumber(value string) string {
	value = strings.TrimLeft(value, "0")
	if value == "" {
		return "0"
	}
	return value
}

func compareModFamilyModified(left, right string) int {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	if left == right {
		return 0
	}
	leftTime, leftErr := time.Parse(time.RFC3339Nano, left)
	rightTime, rightErr := time.Parse(time.RFC3339Nano, right)
	if leftErr == nil && rightErr == nil {
		if leftTime.Before(rightTime) {
			return -1
		}
		if leftTime.After(rightTime) {
			return 1
		}
		return 0
	}
	if left < right {
		return -1
	}
	return 1
}

func normalizeModFamilyText(value string) string {
	var builder strings.Builder
	builder.Grow(len(value))
	space := false
	for _, char := range value {
		if unicode.IsSpace(char) {
			space = builder.Len() > 0
			continue
		}
		if space {
			builder.WriteByte(' ')
			space = false
		}
		builder.WriteRune(unicode.ToLower(char))
	}
	return strings.TrimSpace(builder.String())
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
