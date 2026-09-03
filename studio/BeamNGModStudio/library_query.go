package main

import (
	"context"
	"strings"
	"unicode"
	"unicode/utf8"
)

type librarySearchTerm struct {
	scope string
	value string
}

type librarySearchQuery struct {
	status string
	terms  []librarySearchTerm
}

func parseLibrarySearchQuery(input string) librarySearchQuery {
	var result librarySearchQuery
	activeScope := "all"
	for _, token := range tokenizeLibrarySearchQuery(input) {
		lower := strings.ToLower(token)
		if strings.HasPrefix(lower, "is:") {
			if status := normalizeLibraryStatus(token[len("is:"):]); status != "" {
				result.status = status
				continue
			}
		}
		if strings.HasPrefix(lower, "in:") {
			if scope := normalizeLibraryScope(token[len("in:"):]); scope != "" {
				activeScope = scope
				continue
			}
		}
		if separator := strings.IndexByte(token, ':'); separator > 0 {
			operator := strings.ToLower(strings.TrimSpace(token[:separator]))
			scope := normalizeLibraryScope(operator)
			if scope != "" {
				value := strings.TrimSpace(token[separator+1:])
				if value != "" {
					// These operators are complete one-token expressions. Keep
					// the existing active scope for ordinary terms instead of
					// making their scoped value apply to later text.
					if operator == "tag" || operator == "tags" {
						scope = "tag_exact"
					}
					if operator == "source" || operator == "tag" || operator == "tags" {
						result.terms = append(result.terms, librarySearchTerm{scope: scope, value: strings.ToLower(value)})
						continue
					}
				}
				// Bare inline forms retain the historical sticky behavior.
				activeScope = scope
				if value != "" {
					result.terms = append(result.terms, librarySearchTerm{scope: scope, value: strings.ToLower(value)})
				}
				continue
			}
		}
		value := strings.ToLower(strings.TrimSpace(token))
		if value != "" {
			result.terms = append(result.terms, librarySearchTerm{scope: activeScope, value: value})
		}
	}
	return result
}

func tokenizeLibrarySearchQuery(input string) []string {
	result := []string{}
	var token strings.Builder
	quoted := false
	escaped := false
	flush := func() {
		if value := strings.TrimSpace(token.String()); value != "" {
			result = append(result, value)
		}
		token.Reset()
	}
	for _, char := range input {
		if escaped {
			token.WriteRune(char)
			escaped = false
			continue
		}
		if char == '\\' && quoted {
			escaped = true
			continue
		}
		if char == '"' {
			quoted = !quoted
			continue
		}
		if unicode.IsSpace(char) && !quoted {
			flush()
			continue
		}
		token.WriteRune(char)
	}
	if escaped {
		token.WriteByte('\\')
	}
	flush()
	return result
}

func normalizeLibraryScope(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "all", "any":
		return "all"
	case "tag", "tags":
		return "tag"
	case "kind", "type":
		return "kind"
	case "author":
		return "author"
	case "name", "title":
		return "name"
	case "path", "file":
		return "path"
	case "namespace":
		return "namespace"
	case "collection", "collections", "folder", "folders":
		return "collection"
	case "source", "archive":
		return "source"
	default:
		return ""
	}
}

func normalizeLibraryStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "safe", "clean":
		return "safe"
	case "review", "needs-review", "needs_review":
		return "review"
	case "threat", "unsafe", "infected":
		return "threat"
	case "broken", "invalid":
		return "broken"
	case "unscanned", "not-scanned", "not_scanned":
		return "unscanned"
	case "failed", "scan-failed", "scan_failed":
		return "scan_failed"
	case "scanning":
		return "scanning"
	default:
		return ""
	}
}

func (query librarySearchQuery) matches(item LibraryItem, collectionNames map[string]string) bool {
	if query.status != "" && item.HealthStatus != query.status {
		return false
	}
	for _, term := range query.terms {
		if !librarySearchTermMatches(item, collectionNames[item.FolderID], term) {
			return false
		}
	}
	return true
}

func librarySearchTermMatches(item LibraryItem, collectionName string, term librarySearchTerm) bool {
	contains := func(value string) bool { return strings.Contains(strings.ToLower(value), term.value) }
	containsAny := func(values []string) bool {
		for _, value := range values {
			if contains(value) {
				return true
			}
		}
		return false
	}
	tagNames := make([]string, 0, len(item.Tags))
	for _, tag := range item.Tags {
		tagNames = append(tagNames, tag.Name)
	}
	switch term.scope {
	case "tag_exact":
		for _, tagName := range tagNames {
			if strings.EqualFold(tagName, term.value) {
				return true
			}
		}
		return false
	case "tag":
		return containsAny(tagNames)
	case "kind":
		return contains(string(item.Kind)) || contains(libraryKindSearchName(string(item.Kind)))
	case "author":
		return contains(item.Manifest.Author)
	case "name":
		return contains(item.DisplayName)
	case "path":
		return contains(item.ArchivePath)
	case "namespace":
		return containsAny(namespaceValues(item.Manifest.Namespaces))
	case "collection":
		if term.value == "unfiled" && item.FolderID == "" {
			return true
		}
		return contains(collectionName)
	case "source":
		switch term.value {
		case "available", "linked", "present":
			return item.Linked
		case "missing", "unlinked", "unavailable":
			return !item.Linked
		default:
			return librarySourceSearchMatches(item, term.value)
		}
	default:
		return contains(item.DisplayName) || contains(item.ArchivePath) || contains(item.Manifest.Author) || contains(string(item.Kind)) || contains(libraryKindSearchName(string(item.Kind))) || contains(collectionName) || containsAny(namespaceValues(item.Manifest.Namespaces)) || containsAny(tagNames)
	}
}

func libraryKindSearchName(kind string) string {
	switch kind {
	case "vehicle":
		return "vehicle car"
	case "map":
		return "map level"
	case "ui":
		return "ui app interface"
	case "script":
		return "script extension"
	case "mixed":
		return "mixed"
	default:
		return "unknown"
	}
}

func librarySourceSearchValues(value string) []string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "beamng-repository", "beamng repository", "beamng", "repository", "official", "official repository":
		return []string{"beamng-repository", "beamng repository", "repository", "official", "official repository"}
	case "user-added", "user added", "user", "local", "third-party", "third party":
		return []string{"user-added", "user added", "user", "local", "third-party", "third party"}
	default:
		if value == "" {
			return nil
		}
		return []string{value}
	}
}

func librarySourceSearchMatches(item LibraryItem, value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return false
	}
	sourceID := strings.ToLower(strings.TrimSpace(item.SourceID))
	sourceLabel := strings.ToLower(strings.TrimSpace(item.Source))
	if strings.Contains(sourceID, value) || strings.Contains(sourceLabel, value) {
		return true
	}
	for _, candidate := range librarySourceSearchValues(value) {
		if sourceID == candidate || sourceLabel == candidate {
			return true
		}
	}
	return false
}

func librarySourceCandidate(value string) (string, []any) {
	values := librarySourceSearchValues(value)
	if len(values) == 0 {
		return "0", nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(values)), ",")
	condition := `(lower(COALESCE(sc.id,'')) IN (` + placeholders + `)`
	condition += ` OR lower(COALESCE(sc.label,'')) IN (` + placeholders + `)`
	condition += ` OR lower(COALESCE(sc.id,'')) LIKE ? ESCAPE '\'`
	condition += ` OR lower(COALESCE(sc.label,'')) LIKE ? ESCAPE '\')`
	args := make([]any, 0, len(values)*2+2)
	for _, candidate := range values {
		args = append(args, candidate)
	}
	for _, candidate := range values {
		args = append(args, candidate)
	}
	pattern := libraryLikePattern(value)
	args = append(args, pattern, pattern)
	return condition, args
}

func (s *Store) libraryCollectionNames(ctx context.Context) (map[string]string, error) {
	return s.libraryCollectionNamesQuery(ctx, s.db)
}

func (s *Store) libraryCollectionNamesTx(ctx context.Context, queryer libraryQueryer) (map[string]string, error) {
	return s.libraryCollectionNamesQuery(ctx, queryer)
}

func (s *Store) libraryCollectionNamesQuery(ctx context.Context, queryer libraryQueryer) (map[string]string, error) {
	rows, err := queryer.QueryContext(ctx, `SELECT id,name FROM library_folders`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]string{}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		result[id] = name
	}
	return result, rows.Err()
}

// listLibraryQuery returns the ordered entity IDs that can satisfy a library
// query. It deliberately returns candidates rather than hydrated items:
// health is derived from the latest virus scan and manifest issues, so the
// caller must hydrate each ID and apply the existing Go-level health/query
// semantics before returning it to a caller.
//
// Searchable text is reduced by SQLite's aggregate FTS index. Source terms
// additionally use the relational source classification table so aliases and
// retained labels are matched independently of archive paths. A residual
// match is still required by callers for scoped terms because the aggregate
// index intentionally does not encode the query language's scope state.
func (s *Store) listLibraryQuery(ctx context.Context, health, kind, query, folderID string) ([]string, error) {
	return s.listLibraryQueryQuery(ctx, s.db, health, kind, query, folderID)
}

func (s *Store) listLibraryQueryTx(ctx context.Context, queryer libraryQueryer, health, kind, query, folderID string) ([]string, error) {
	return s.listLibraryQueryQuery(ctx, queryer, health, kind, query, folderID)
}

func (s *Store) listLibraryQueryQuery(ctx context.Context, queryer libraryQueryer, health, kind, query, folderID string) ([]string, error) {
	// Health values are derived after canonical hydration. Normalize here so
	// callers get the same treatment for invalid values as the old in-memory
	// path, while keeping this candidate query independent of scan history.
	_ = normalizeLibraryStatus(health)

	search := parseLibrarySearchQuery(query)
	conditions := []string{"1=1"}
	args := make([]any, 0, len(search.terms)*2+4)

	if kind != "" && kind != "all" {
		conditions = append(conditions, "e.kind = ?")
		args = append(args, kind)
	}
	switch {
	case folderID == "unfiled":
		conditions = append(conditions, "COALESCE(lfe.folder_id, '') = ''")
	case folderID != "" && folderID != "all":
		conditions = append(conditions, "COALESCE(lfe.folder_id, '') = ?")
		args = append(args, folderID)
	}

	for _, term := range search.terms {
		switch term.scope {
		case "source":
			switch term.value {
			case "available", "linked", "present":
				conditions = append(conditions, "COALESCE(l.active, 0) = 1")
				continue
			case "missing", "unlinked", "unavailable":
				conditions = append(conditions, "COALESCE(l.active, 0) = 0")
				continue
			default:
				condition, sourceArgs := librarySourceCandidate(term.value)
				conditions = append(conditions, condition)
				args = append(args, sourceArgs...)
				continue
			}
		case "collection":
			if term.value == "unfiled" {
				conditions = append(conditions, "COALESCE(lfe.folder_id, '') = ''")
				continue
			}
			// Include the FTS candidate and a relational fallback. The latter
			// keeps folder renames immediately visible even if a maintenance
			// operation has not rebuilt the aggregate index yet.
			condition, argument := libraryFTSCandidate(term.value)
			conditions = append(conditions, "("+condition+" OR lower(COALESCE(lf.name, '')) LIKE ? ESCAPE '\\')")
			args = append(args, argument, libraryLikePattern(term.value))
			continue
		case "tag_exact", "tag":
			// Tag assignments and renames are relational data. Keep the FTS
			// path for indexed candidate selection, with an EXISTS fallback
			// so a just-renamed tag remains searchable in the same snapshot.
			// For tag_exact, hydration applies the final exact-name check.
			condition, argument := libraryFTSCandidate(term.value)
			conditions = append(conditions, "("+condition+` OR EXISTS (
				SELECT 1 FROM mod_tag_entities mte
				JOIN mod_tags mt ON mt.id = mte.tag_id
				WHERE mte.entity_id = e.id
					AND lower(mt.name) LIKE ? ESCAPE '\'
			))`)
			args = append(args, argument, libraryLikePattern(term.value))
			continue
		case "kind":
			// The aggregate index normally contains both the stored kind and
			// its human-facing aliases. The direct kind values are a cheap
			// fallback for an index built before an alias was introduced.
			condition, argument := libraryFTSCandidate(term.value)
			kindValues := libraryKindValuesForSearch(term.value)
			conditions = append(conditions, "("+condition+")")
			// Keep the MATCH or short-term fallback argument ahead of the
			// direct kind values in the positional argument list.
			args = append(args, argument)
			if len(kindValues) > 0 {
				placeholders := strings.TrimRight(strings.Repeat("?,", len(kindValues)), ",")
				conditions[len(conditions)-1] = "(" + condition + " OR e.kind IN (" + placeholders + "))"
				for _, value := range kindValues {
					args = append(args, value)
				}
			}
			continue
		default:
			condition, argument := libraryFTSCandidate(term.value)
			conditions = append(conditions, condition)
			args = append(args, argument)
		}
	}

	sqlQuery := `SELECT DISTINCT e.id
		FROM entities e
		LEFT JOIN library_folder_entities lfe ON lfe.entity_id = e.id
		LEFT JOIN library_folders lf ON lf.id = lfe.folder_id
		LEFT JOIN archive_links l ON l.id = (
			SELECT l2.id FROM archive_links l2
			WHERE l2.entity_id = e.id
			ORDER BY l2.active DESC, l2.last_seen_at DESC
			LIMIT 1
		)
		LEFT JOIN source_classifications sc ON sc.id = COALESCE(NULLIF(l.source_id,''),NULLIF(e.source_id,''),'user-added')
		WHERE ` + strings.Join(conditions, " AND ") + `
		ORDER BY COALESCE(l.active, 0) DESC, e.updated_at DESC, e.display_name COLLATE NOCASE`
	rows, err := queryer.QueryContext(ctx, sqlQuery, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entityIDs := make([]string, 0)
	for rows.Next() {
		var entityID string
		if err := rows.Scan(&entityID); err != nil {
			return nil, err
		}
		entityIDs = append(entityIDs, entityID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return entityIDs, nil
}

// libraryFTSCandidate keeps the indexed path for normal terms while handling
// short terms exactly like the old strings.Contains matcher. FTS5 trigram
// tokenization cannot reliably answer one- or two-rune substring queries, so
// those candidates use the aggregate content column's escaped LIKE fallback;
// the residual Go matcher still decides the final scoped result.
//
// Keep the FTS lookup uncorrelated with the outer entity scan. An EXISTS that
// compares library_search_fts.entity_id to e.id makes SQLite reevaluate the
// virtual-table scan for every entity. Building an entity-ID membership list
// lets FTS5 execute MATCH (or the short-term LIKE fallback) once, bounded by
// matching rows, before the outer query applies its relational filters.
func libraryFTSCandidate(value string) (string, any) {
	if utf8.RuneCountInString(strings.TrimSpace(value)) < 3 {
		return `e.id IN (
			SELECT library_search_fts.entity_id
			FROM library_search_fts
			WHERE lower(library_search_fts.content) LIKE ? ESCAPE '\'
		)`, libraryLikePattern(value)
	}
	return `e.id IN (
		SELECT library_search_fts.entity_id
		FROM library_search_fts
		WHERE library_search_fts MATCH ?
	)`, libraryFTSMatch(value)
}

func libraryFTSMatch(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return `""`
	}
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func libraryLikePattern(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	value = strings.ReplaceAll(value, `_`, `\_`)
	return "%" + value + "%"
}

func libraryKindValuesForSearch(value string) []string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return nil
	}
	values := make([]string, 0, 2)
	for _, kind := range []string{"vehicle", "map", "ui", "script", "mixed", "unknown"} {
		if strings.Contains(kind, value) || strings.Contains(strings.ToLower(libraryKindSearchName(kind)), value) {
			values = append(values, kind)
		}
	}
	return values
}
