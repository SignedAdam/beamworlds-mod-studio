package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	modkit "github.com/SignedAdam/beamworlds-modkit"
)

// ---------------------------------------------------------------------------
// Manifest cache — keyed on (artifactID, summary revision)
// ---------------------------------------------------------------------------

// artifactManifestEntry holds one cached parse of an artifact's full manifest.
type artifactManifestEntry struct {
	revision    string
	manifest    modkit.Manifest
	sourceBytes int
}

// artifactManifestCache is a bounded, revision-checked manifest cache.  Values
// are never shared mutably: every Get returns a deep-copied Manifest.
type artifactManifestCache struct {
	mu      sync.Mutex
	entries map[string]*artifactManifestEntry // keyed by artifact_id
	order   []string                          // insertion order for eviction
	limit   int
	bytes   int
}

func newArtifactManifestCache(limit int) *artifactManifestCache {
	if limit <= 0 {
		limit = 2048
	}
	return &artifactManifestCache{
		entries: make(map[string]*artifactManifestEntry, limit),
		order:   make([]string, 0, limit),
		limit:   limit,
	}
}

// Retention is bounded by both entry count and original JSON size. This is a
// source-data budget, not a claim about Go heap size; parsed objects vary.
const manifestCacheSourceBudget = 64 << 20

// get returns a deep-copied manifest if the revision matches.
func (c *artifactManifestCache) get(artifactID, revision string) (modkit.Manifest, bool) {
	if c == nil || artifactID == "" || revision == "" {
		return modkit.Manifest{}, false
	}
	c.mu.Lock()
	entry, ok := c.entries[artifactID]
	c.mu.Unlock()
	if !ok || entry.revision != revision {
		return modkit.Manifest{}, false
	}
	return cloneManifest(entry.manifest), true
}

// put stores a manifest under the given revision.  The manifest is cloned
// before storage so the caller may continue to mutate its copy.
func (c *artifactManifestCache) put(artifactID, revision string, manifest modkit.Manifest, sourceBytes int) {
	if c == nil || artifactID == "" || revision == "" || sourceBytes > manifestCacheSourceBudget {
		return
	}
	stored := cloneManifest(manifest)
	c.mu.Lock()
	defer c.mu.Unlock()
	if previous, exists := c.entries[artifactID]; exists {
		c.bytes -= previous.sourceBytes
	} else {
		c.order = append(c.order, artifactID)
	}
	c.entries[artifactID] = &artifactManifestEntry{revision: revision, manifest: stored, sourceBytes: sourceBytes}
	c.bytes += sourceBytes
	for (len(c.entries) > c.limit || c.bytes > manifestCacheSourceBudget) && len(c.order) > 0 {
		oldest := c.order[0]
		c.order = c.order[1:]
		c.bytes -= c.entries[oldest].sourceBytes
		delete(c.entries, oldest)
	}
}

func (c *artifactManifestCache) clear() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]*artifactManifestEntry)
	c.order = nil
	c.bytes = 0
}

// Only cache misses read the large manifest column. The caller's transaction
// binds revisions to the same snapshot as the entity/link rows.
func (s *Store) hydrateMissingManifests(ctx context.Context, queryer libraryQueryer, items []LibraryItem, missing map[string][]int, revisions map[string]string) error {
	ids := make([]string, 0, len(missing))
	for id := range missing {
		ids = append(ids, id)
	}
	const chunkSize = 800
	for start := 0; start < len(ids); start += chunkSize {
		chunk := ids[start:min(start+chunkSize, len(ids))]
		args := make([]any, len(chunk))
		for index, id := range chunk {
			args[index] = id
		}
		rows, err := queryer.QueryContext(ctx, `SELECT a.id,COALESCE(sm.revision,''),a.manifest_json
			FROM artifacts a LEFT JOIN artifact_summaries sm ON sm.artifact_id=a.id
			WHERE a.id IN (`+strings.TrimRight(strings.Repeat("?,", len(chunk)), ",")+`)`, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id, revision, payload string
			if err := rows.Scan(&id, &revision, &payload); err != nil {
				_ = rows.Close()
				return err
			}
			if revision != revisions[id] {
				_ = rows.Close()
				return fmt.Errorf("archive metadata changed while reading %s", id)
			}
			if err := ctx.Err(); err != nil {
				_ = rows.Close()
				return err
			}
			var manifest modkit.Manifest
			if err := json.Unmarshal([]byte(payload), &manifest); err != nil {
				_ = rows.Close()
				return err
			}
			s.manifestCache.put(id, revision, manifest, len(payload))
			for index, itemIndex := range missing[id] {
				if index == 0 {
					items[itemIndex].Manifest = manifest
				} else {
					items[itemIndex].Manifest = cloneManifest(manifest)
				}
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	return nil
}

// cloneManifest returns a mutation-isolated copy of a Manifest.  Nested
// slices and maps are deep-copied so shared cache entries cannot be mutated
// through returned values.
func cloneManifest(m modkit.Manifest) modkit.Manifest {
	c := m
	if m.ContentTags != nil {
		c.ContentTags = make([]string, len(m.ContentTags))
		copy(c.ContentTags, m.ContentTags)
	}
	if m.Namespaces != nil {
		c.Namespaces = cloneStringMap(m.Namespaces)
	}
	if m.Members != nil {
		c.Members = make([]modkit.ArchiveMember, len(m.Members))
		copy(c.Members, m.Members)
	}
	if m.MetadataDocuments != nil {
		c.MetadataDocuments = make([]modkit.MetadataDocument, len(m.MetadataDocuments))
		for i, md := range m.MetadataDocuments {
			c.MetadataDocuments[i] = md
			if md.Data != nil {
				c.MetadataDocuments[i].Data = cloneAnyMap(md.Data)
			}
		}
	}
	if m.Images != nil {
		c.Images = make([]modkit.ImageCandidate, len(m.Images))
		copy(c.Images, m.Images)
	}
	if m.Variants != nil {
		c.Variants = make([]modkit.Variant, len(m.Variants))
		for i, v := range m.Variants {
			c.Variants[i] = v
			if v.Fields != nil {
				c.Variants[i].Fields = cloneAnyMap(v.Fields)
			}
		}
	}
	if m.Issues != nil {
		c.Issues = make([]modkit.Issue, len(m.Issues))
		copy(c.Issues, m.Issues)
	}
	c.JBeam = m.JBeam
	if m.JBeam.Controllers != nil {
		c.JBeam.Controllers = make([]string, len(m.JBeam.Controllers))
		copy(c.JBeam.Controllers, m.JBeam.Controllers)
	}
	c.Map = m.Map
	if m.Map.LevelIDs != nil {
		c.Map.LevelIDs = make([]string, len(m.Map.LevelIDs))
		copy(c.Map.LevelIDs, m.Map.LevelIDs)
	}
	c.UI = m.UI
	if m.UI.AppRoots != nil {
		c.UI.AppRoots = make([]string, len(m.UI.AppRoots))
		copy(c.UI.AppRoots, m.UI.AppRoots)
	}
	if m.SharedAssets != nil {
		sa := *m.SharedAssets
		c.SharedAssets = &sa
	}
	return c
}

func cloneAnyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = cloneManifestValue(v)
	}
	return out
}

func cloneManifestValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		return cloneAnyMap(value)
	case []any:
		copy := make([]any, len(value))
		for index, child := range value {
			copy[index] = cloneManifestValue(child)
		}
		return copy
	default:
		return value
	}
}

// ---------------------------------------------------------------------------
// Summary-only library item hydration
// ---------------------------------------------------------------------------

// listLibrarySummaryItemsByIDsTx returns library items with all scalar fields,
// real count columns, tags, collections, and security state populated from the
// lightweight artifact_summaries projection.  The Manifest carries only Title,
// Author, Version, Description and Namespaces for residual search. Issues are
// read to derive health, then omitted from rows; detailed inspection data is
// available through GetEntity, not resent with every table operation.
func (s *Store) listLibrarySummaryItemsByIDsTx(ctx context.Context, tx *sql.Tx, entityIDs []string) ([]LibraryItem, error) {
	if len(entityIDs) == 0 {
		return []LibraryItem{}, nil
	}
	const chunkSize = 800
	items := make([]LibraryItem, 0, len(entityIDs))
	for start := 0; start < len(entityIDs); start += chunkSize {
		end := start + chunkSize
		if end > len(entityIDs) {
			end = len(entityIDs)
		}
		chunk, err := s.queryLibrarySummaryItemsQuery(ctx, tx, entityIDs[start:end])
		if err != nil {
			return nil, err
		}
		items = append(items, chunk...)
	}
	return items, nil
}

func (s *Store) queryLibrarySummaryItemsQuery(ctx context.Context, queryer libraryQueryer, entityIDs []string) ([]LibraryItem, error) {
	query := `SELECT e.id, e.updated_at, COALESCE(e.archived_at,''), e.display_name, e.kind,
		COALESCE(NULLIF(l.source_id,''),NULLIF(e.source_id,''),'user-added'),
		CASE lower(COALESCE(NULLIF(l.source_id,''),NULLIF(e.source_id,''),'user-added'))
			WHEN 'beamng-repository' THEN 'BeamNG Repository'
			WHEN 'user-added' THEN 'User added'
			ELSE COALESCE(sc.label,'')
		END,
		COALESCE(l.id,''), COALESCE(l.artifact_id,''), COALESCE(l.path,''), COALESCE(l.root_path,''),
		COALESCE(l.active,0), COALESCE(l.size_bytes,0), COALESCE(l.modified_at,''), COALESCE(l.last_seen_at,''),
		COALESCE(a.central_fingerprint,''), COALESCE(a.sha256,''),
		COALESCE(ast.sha256,''),
		COALESCE(s.title,''), COALESCE(s.author,''), COALESCE(s.version,''),
		COALESCE(s.description,''), COALESCE(s.namespaces_json,'{}'), COALESCE(s.issues_json,'[]'),
		COALESCE(s.member_count,0), COALESCE(s.namespace_count,0),
		COALESCE(s.variant_count,0), COALESCE(s.issue_count,0)
	FROM entities e
	LEFT JOIN archive_links l ON l.id = (
		SELECT l2.id FROM archive_links l2
		WHERE l2.entity_id=e.id
		ORDER BY l2.active DESC, l2.last_seen_at DESC, l2.id DESC LIMIT 1
	)
	LEFT JOIN source_classifications sc ON sc.id = COALESCE(NULLIF(l.source_id,''),NULLIF(e.source_id,''),'user-added')
	LEFT JOIN artifacts a ON a.id=l.artifact_id
	LEFT JOIN artifact_summaries s ON s.artifact_id=a.id
	LEFT JOIN entity_assets ea ON ea.entity_id=e.id AND ea.role='thumbnail' AND ea.ordinal=0
	LEFT JOIN assets ast ON ast.sha256=ea.asset_sha256`

	args := make([]any, 0, len(entityIDs))
	if len(entityIDs) > 0 {
		placeholders := strings.TrimRight(strings.Repeat("?,", len(entityIDs)), ",")
		query += ` WHERE e.id IN (` + placeholders + `)`
		for _, entityID := range entityIDs {
			args = append(args, entityID)
		}
	}
	query += ` ORDER BY l.active DESC, e.updated_at DESC, e.display_name COLLATE NOCASE, e.id`

	rows, err := queryer.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]LibraryItem, 0, len(entityIDs))
	for rows.Next() {
		var item LibraryItem
		var kind, assetSHA string
		var title, author, version, description, namespacesJSON, issuesJSON string
		if err := rows.Scan(
			&item.EntityID, &item.Revision, &item.ArchivedAt, &item.DisplayName, &kind,
			&item.SourceID, &item.Source,
			&item.LinkID, &item.ArtifactID, &item.ArchivePath, &item.RootPath,
			&item.Linked, &item.SizeBytes, &item.ModifiedAt, &item.LastSeenAt,
			&item.Fingerprint, &item.SHA256,
			&assetSHA,
			&title, &author, &version, &description, &namespacesJSON, &issuesJSON,
			&item.MemberCount, &item.NamespaceCount, &item.VariantCount, &item.IssueCount,
		); err != nil {
			return nil, err
		}
		item.Kind = modkit.Kind(kind)
		item.Manifest.Title = title
		item.Manifest.Author = author
		item.Manifest.Version = version
		item.Manifest.Description = description
		item.Manifest.Kind = item.Kind
		if namespacesJSON != "" && namespacesJSON != "{}" {
			if err := json.Unmarshal([]byte(namespacesJSON), &item.Manifest.Namespaces); err != nil {
				return nil, err
			}
		}
		if issuesJSON != "" && issuesJSON != "[]" {
			if err := json.Unmarshal([]byte(issuesJSON), &item.Manifest.Issues); err != nil {
				return nil, err
			}
		}
		item.CollectionIDs = []string{}
		if assetSHA != "" {
			item.ThumbnailURL = "/cache/" + assetSHA
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := attachLibraryItemCollectionsQuery(ctx, queryer, items); err != nil {
		return nil, err
	}
	if err := attachLibraryItemTagsQuery(ctx, queryer, items); err != nil {
		return nil, err
	}
	if err := attachLibraryItemHealthQuery(ctx, queryer, items); err != nil {
		return nil, err
	}
	for index := range items {
		items[index].Manifest.Issues = nil
	}
	return items, nil
}

// ---------------------------------------------------------------------------
// Scoped health hydration — queries only the requested entity IDs
// ---------------------------------------------------------------------------

// attachLibraryItemHealthQuery limits scan history to the requested entities.
func attachLibraryItemHealthQuery(ctx context.Context, queryer libraryQueryer, items []LibraryItem) error {
	for index := range items {
		items[index].HealthStatus = "unscanned"
		if libraryItemBroken(items[index]) {
			items[index].HealthStatus = "broken"
		}
		items[index].HealthLabel = virusHealthLabel(items[index].HealthStatus)
		items[index].LastSecurityScanAt = ""
		items[index].LastSecurityScanVerdict = ""
		items[index].LastSecurityScanSHA256 = ""
		items[index].SecurityScanChanged = false
	}
	if len(items) == 0 {
		return nil
	}

	type healthRecord struct {
		artifactID, status, verdict, updatedAt, sha256 string
	}
	currentHealth := map[string]healthRecord{}
	latestHealth := map[string]healthRecord{}

	const chunkSize = 800
	entityIDs := make([]string, 0, len(items))
	for i := range items {
		entityIDs = append(entityIDs, items[i].EntityID)
	}

	for start := 0; start < len(entityIDs); start += chunkSize {
		end := start + chunkSize
		if end > len(entityIDs) {
			end = len(entityIDs)
		}
		chunk := entityIDs[start:end]
		placeholders := strings.TrimRight(strings.Repeat("?,", len(chunk)), ",")
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		rows, err := queryer.QueryContext(ctx,
			`SELECT entity_id,artifact_id,status,verdict,updated_at,artifact_sha256,current_ordinal,entity_ordinal FROM (
				SELECT v.entity_id,v.artifact_id,v.status,v.verdict,v.updated_at,
					COALESCE(NULLIF(v.file_sha256,''),a.sha256,'') AS artifact_sha256,
					ROW_NUMBER() OVER(PARTITION BY v.entity_id,v.artifact_id ORDER BY v.updated_at DESC,v.id DESC) AS current_ordinal,
					ROW_NUMBER() OVER(PARTITION BY v.entity_id ORDER BY v.updated_at DESC,v.id DESC) AS entity_ordinal
				FROM virus_scans v
				LEFT JOIN artifacts a ON a.id=v.artifact_id
				WHERE v.entity_id IN (`+placeholders+`)
			) WHERE current_ordinal=1 OR entity_ordinal=1`, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var entityID, artifactID string
			var record healthRecord
			var currentOrdinal, entityOrdinal int
			if err := rows.Scan(&entityID, &artifactID, &record.status, &record.verdict, &record.updatedAt, &record.sha256, &currentOrdinal, &entityOrdinal); err != nil {
				_ = rows.Close()
				return err
			}
			record.artifactID = artifactID
			if currentOrdinal == 1 {
				currentHealth[entityID+"\x00"+artifactID] = record
			}
			if entityOrdinal == 1 {
				latestHealth[entityID] = record
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}

	scanStatus := func(record healthRecord) string {
		status := record.verdict
		if record.status == "running" {
			status = "scanning"
		} else if record.status == "failed" {
			status = "scan_failed"
		}
		if status == "" {
			status = "unscanned"
		}
		return status
	}
	for index := range items {
		item := &items[index]
		if record, ok := latestHealth[item.EntityID]; ok {
			item.LastSecurityScanAt = record.updatedAt
			item.LastSecurityScanVerdict = record.verdict
			item.LastSecurityScanSHA256 = record.sha256
			item.SecurityScanChanged = record.artifactID != item.ArtifactID
			if !item.SecurityScanChanged && record.sha256 != "" && item.SHA256 != "" {
				item.SecurityScanChanged = !strings.EqualFold(record.sha256, item.SHA256)
			}
		}
		record, ok := currentHealth[item.EntityID+"\x00"+item.ArtifactID]
		if !ok {
			continue
		}
		status := scanStatus(record)
		if status != "threat" && libraryItemBroken(*item) {
			status = "broken"
		}
		item.HealthStatus = status
		item.HealthLabel = virusHealthLabel(status)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Summary-backed AppService.ListLibrary
// ---------------------------------------------------------------------------

// ListLibrarySummary is the summary-only listing route for the frontend.
// It uses the same SQL query pipeline as ListLibrary but hydrates with the
// lightweight artifact_summaries projection instead of parsing full manifests.
func (s *Store) ListLibrarySummary(ctx context.Context, health, kind, query, collectionID, scope string) ([]LibraryItem, error) {
	archiveScope, err := normalizeLibraryArchiveScope(scope)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	entityIDs, err := s.listLibraryQueryTx(ctx, tx, health, kind, query, collectionID, archiveScope)
	if err != nil {
		return nil, err
	}
	items, err := s.listLibrarySummaryItemsByIDsTx(ctx, tx, entityIDs)
	if err != nil {
		return nil, err
	}
	byEntity := make(map[string]int, len(items))
	for index := range items {
		byEntity[items[index].EntityID] = index
	}
	search := parseLibrarySearchQuery(query)
	collectionNames := map[string]string{}
	effectiveCollectionNames := map[string][]string{}
	needsCollectionNames := false
	for _, term := range search.terms {
		if term.scope == "all" || term.scope == "collection" {
			needsCollectionNames = true
			break
		}
	}
	if needsCollectionNames {
		collectionNames, err = s.libraryCollectionNamesTx(ctx, tx)
		if err != nil {
			return nil, err
		}
		effectiveCollectionNames, err = libraryEffectiveCollectionNamesTx(ctx, tx, entityIDs)
		if err != nil {
			return nil, err
		}
	}
	health = normalizeLibraryStatus(health)
	filtered := make([]LibraryItem, 0, len(entityIDs))
	for _, entityID := range entityIDs {
		itemIndex, ok := byEntity[entityID]
		if !ok {
			continue
		}
		item := items[itemIndex]
		if health != "" && item.HealthStatus != health {
			continue
		}
		if kind != "" && kind != "all" && string(item.Kind) != kind {
			continue
		}
		if collectionID == "unfiled" && len(item.CollectionIDs) > 0 {
			continue
		}
		if (search.status != "" || len(search.terms) > 0) &&
			!search.matches(item, collectionNames, effectiveCollectionNames[entityID]) {
			continue
		}
		filtered = append(filtered, item)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return filtered, nil
}
