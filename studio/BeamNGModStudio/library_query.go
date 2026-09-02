package main

import (
	"context"
	"strings"
	"unicode"
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
			scope := normalizeLibraryScope(token[:separator])
			if scope != "" {
				value := strings.TrimSpace(token[separator+1:])
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
	default:
		return ""
	}
}

func normalizeLibraryStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "linked", "available":
		return "linked"
	case "missing", "unlinked":
		return "missing"
	default:
		return ""
	}
}

func (query librarySearchQuery) matches(item LibraryItem, collectionNames map[string]string) bool {
	if query.status == "linked" && !item.Linked || query.status == "missing" && item.Linked {
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

func (s *Store) libraryCollectionNames(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name FROM library_folders`)
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
