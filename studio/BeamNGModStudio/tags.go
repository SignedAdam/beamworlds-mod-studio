package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	modkit "github.com/SignedAdam/beamworlds-modkit"
)

const (
	exampleTagSeedKey          = "library_example_tags_seeded_v1"
	terrainTagSeedKey          = "library_default_terrain_tag_seeded_v1"
	gameplayGraphicsTagSeedKey = "library_default_gameplay_graphics_tags_seeded_v1"
	trailerTagSeedKey          = "library_default_trailer_tag_seeded_v1"
	defaultModTagColor         = "#7a8791"
	defaultModTagIcon          = "tag"
)

var exampleModTagNames = []string{
	"Car",
	"Motorbike",
	"Trailer",
	"UI",
	"Gameplay Overhaul",
	"Gameplay",
	"Graphics",
	"Vehicle Effects",
	"Boat",
	"Props",
	"Airplane",
	"Helicopter",
	"Mission",
	"Terrain",
}

type ModTag struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Color    string `json:"color"`
	Icon     string `json:"icon"`
	Origin   string `json:"origin"`
	ModCount int    `json:"modCount"`
}

func normalizeModTagColor(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) != 7 || value[0] != '#' {
		return "", errors.New("tag color must be a #RRGGBB value")
	}
	for _, char := range value[1:] {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return "", errors.New("tag color must be a #RRGGBB value")
		}
	}
	return value, nil
}

// Presentation-only glyph names. The Tag editor offers exactly this set, so both sides
// must be extended together; anything else is rejected rather than silently rewritten.
var modTagIcons = []string{
	"tag", "vehicle", "wheel", "truck", "plane", "helicopter", "boat", "map",
	"mountain", "globe", "flag", "code", "layout", "audio", "bulb", "box",
	"brush", "wrench", "gauge", "files", "shield", "star", "user",
}

func normalizeModTagIcon(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if slices.Contains(modTagIcons, value) {
		return value, nil
	}
	return "", fmt.Errorf("tag icon must be one of %s", strings.Join(modTagIcons, ", "))
}

func (service *AppService) CreateModTag(name, color, icon string) (OrganizationState, error) {
	if err := service.store.CreateModTag(context.Background(), name, color, icon); err != nil {
		return OrganizationState{}, err
	}
	return service.Organization()
}

func (service *AppService) UpdateModTagVisual(tagID, color, icon string) (OrganizationState, error) {
	if err := service.store.UpdateModTagVisual(context.Background(), tagID, color, icon); err != nil {
		return OrganizationState{}, err
	}
	return service.Organization()
}

func (service *AppService) RenameModTag(tagID, name string) (OrganizationState, error) {
	if err := service.store.RenameModTag(context.Background(), tagID, name); err != nil {
		return OrganizationState{}, err
	}
	return service.Organization()
}

func (service *AppService) DeleteModTag(tagID string) (OrganizationState, error) {
	ctx := context.Background()
	if err := service.store.DeleteModTag(ctx, tagID); err != nil {
		return OrganizationState{}, err
	}
	return service.Organization()
}

func (service *AppService) SetLibraryItemTags(entityID string, tagIDs []string) (LibraryItem, error) {
	ctx := context.Background()
	if err := service.store.SetLibraryItemTags(ctx, entityID, tagIDs); err != nil {
		return LibraryItem{}, err
	}
	return service.store.GetLibraryItem(ctx, entityID)
}

func (s *Store) ensureModTagVisualColumns(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(mod_tags)`)
	if err != nil {
		return err
	}
	hasColor, hasIcon := false, false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return err
		}
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "color":
			hasColor = true
		case "icon":
			hasIcon = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if !hasColor {
		if _, err := s.db.ExecContext(ctx, `ALTER TABLE mod_tags ADD COLUMN color TEXT NOT NULL DEFAULT '#7a8791'`); err != nil {
			return err
		}
	}
	if !hasIcon {
		if _, err := s.db.ExecContext(ctx, `ALTER TABLE mod_tags ADD COLUMN icon TEXT NOT NULL DEFAULT 'tag'`); err != nil {
			return err
		}
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE mod_tags SET color=? WHERE color IS NULL OR TRIM(color)=''`, defaultModTagColor); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE mod_tags SET icon=? WHERE icon IS NULL OR TRIM(icon)=''`, defaultModTagIcon)
	return err
}

func (s *Store) ensureExampleModTags(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var seeded string
	err = tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, exampleTagSeedKey).Scan(&seeded)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if errors.Is(err, sql.ErrNoRows) {
		for _, name := range exampleModTagNames {
			if _, err := tx.ExecContext(ctx, `INSERT INTO mod_tags(id,name,color,icon,created_at,updated_at) VALUES(?,?,?,?,?,?) ON CONFLICT(name) DO NOTHING`, "example-"+tagIDPart(name), name, defaultModTagColor, defaultModTagIcon, nowUTC(), nowUTC()); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, exampleTagSeedKey, "1"); err != nil {
			return err
		}
	}
	if err := ensureAdditionalDefaultModTagsTx(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

func ensureAdditionalDefaultModTagsTx(ctx context.Context, tx *sql.Tx) error {
	for _, seed := range []struct {
		key   string
		names []string
	}{
		{terrainTagSeedKey, []string{"Terrain"}},
		{gameplayGraphicsTagSeedKey, []string{"Gameplay", "Graphics"}},
		{trailerTagSeedKey, []string{"Trailer"}},
	} {
		var seeded string
		err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, seed.key).Scan(&seeded)
		if err == nil {
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		for _, name := range seed.names {
			if _, err := tx.ExecContext(ctx, `INSERT INTO mod_tags(id,name,color,icon,created_at,updated_at) VALUES(?,?,?,?,?,?) ON CONFLICT(name) DO NOTHING`, "example-"+tagIDPart(name), name, defaultModTagColor, defaultModTagIcon, nowUTC(), nowUTC()); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, seed.key, "1"); err != nil {
			return err
		}
	}
	return nil
}

func tagIDPart(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var result strings.Builder
	dash := false
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' {
			result.WriteRune(char)
			dash = false
		} else if result.Len() > 0 && !dash {
			result.WriteByte('-')
			dash = true
		}
	}
	return strings.TrimRight(result.String(), "-")
}

func (s *Store) listModTags(ctx context.Context) ([]ModTag, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT t.id,t.name,t.color,t.icon,t.origin,COUNT(te.entity_id) FROM mod_tags t LEFT JOIN mod_tag_entities te ON te.tag_id=t.id GROUP BY t.id ORDER BY t.name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []ModTag{}
	for rows.Next() {
		var tag ModTag
		if err := rows.Scan(&tag.ID, &tag.Name, &tag.Color, &tag.Icon, &tag.Origin, &tag.ModCount); err != nil {
			return nil, err
		}
		result = append(result, tag)
	}
	return result, rows.Err()
}

func (s *Store) CreateModTag(ctx context.Context, name, color, icon string) error {
	name, err := cleanOrganizationName(name, "tag name")
	if err != nil {
		return err
	}
	color, err = normalizeModTagColor(color)
	if err != nil {
		return err
	}
	icon, err = normalizeModTagIcon(icon)
	if err != nil {
		return err
	}
	id, err := modkit.NewID()
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO mod_tags(id,name,color,icon,created_at,updated_at) VALUES(?,?,?,?,?,?)`, id, name, color, icon, nowUTC(), nowUTC())
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "unique") {
		return fmt.Errorf("tag %q already exists", name)
	}
	return err
}

func (s *Store) UpdateModTagVisual(ctx context.Context, tagID, color, icon string) error {
	color, err := normalizeModTagColor(color)
	if err != nil {
		return err
	}
	icon, err = normalizeModTagIcon(icon)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE mod_tags SET color=?,icon=?,updated_at=? WHERE id=?`, color, icon, nowUTC(), strings.TrimSpace(tagID))
	return requireChanged(result, err, "tag")
}

func (s *Store) RenameModTag(ctx context.Context, tagID, name string) error {
	name, err := cleanOrganizationName(name, "tag name")
	if err != nil {
		return err
	}
	tagID = strings.TrimSpace(tagID)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT entity_id FROM mod_tag_entities WHERE tag_id=? ORDER BY entity_id`, tagID)
	if err != nil {
		return err
	}
	entityIDs := []string{}
	for rows.Next() {
		var entityID string
		if err := rows.Scan(&entityID); err != nil {
			_ = rows.Close()
			return err
		}
		entityIDs = append(entityIDs, entityID)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE mod_tags SET name=?,updated_at=? WHERE id=?`, name, nowUTC(), tagID)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "unique") {
		return fmt.Errorf("tag %q already exists", name)
	}
	if err := requireChanged(result, err, "tag"); err != nil {
		return err
	}
	for _, entityID := range entityIDs {
		if err := s.refreshLibrarySearchEntryTx(ctx, tx, entityID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) DeleteModTag(ctx context.Context, tagID string) error {
	tagID = strings.TrimSpace(tagID)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var tagName, color, icon string
	if err := tx.QueryRowContext(ctx, `SELECT name,color,icon FROM mod_tags WHERE id=?`, tagID).Scan(&tagName, &color, &icon); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("tag was not found")
		}
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT entity_id FROM mod_tag_entities WHERE tag_id=? ORDER BY entity_id`, tagID)
	if err != nil {
		return err
	}
	entityIDs := []string{}
	for rows.Next() {
		var entityID string
		if err := rows.Scan(&entityID); err != nil {
			rows.Close()
			return err
		}
		entityIDs = append(entityIDs, entityID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, entityID := range entityIDs {
		if err := appendEventTx(ctx, tx, entityID, "tag_removed", map[string]any{
			"tagId": tagID, "tagName": tagName, "color": color, "icon": icon,
		}); err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM mod_tags WHERE id=?`, tagID)
	if err := requireChanged(result, err, "tag"); err != nil {
		return err
	}
	for _, entityID := range entityIDs {
		if err := s.refreshLibrarySearchEntryTx(ctx, tx, entityID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) SetLibraryItemTags(ctx context.Context, entityID string, tagIDs []string) error {
	entityID = strings.TrimSpace(entityID)
	unique := make([]string, 0, len(tagIDs))
	seen := make(map[string]struct{}, len(tagIDs))
	for _, tagID := range tagIDs {
		tagID = strings.TrimSpace(tagID)
		if tagID == "" {
			continue
		}
		if _, exists := seen[tagID]; exists {
			continue
		}
		seen[tagID] = struct{}{}
		unique = append(unique, tagID)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var entity string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM entities WHERE id=?`, entityID).Scan(&entity); err != nil {
		return err
	}
	type tagAssignment struct {
		id, name, color, icon string
	}
	current := make(map[string]tagAssignment)
	currentOrder := make([]string, 0)
	rows, err := tx.QueryContext(ctx, `SELECT t.id,t.name,t.color,t.icon
		FROM mod_tag_entities te JOIN mod_tags t ON t.id=te.tag_id
		WHERE te.entity_id=? ORDER BY t.id`, entityID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var tag tagAssignment
		if err := rows.Scan(&tag.id, &tag.name, &tag.color, &tag.icon); err != nil {
			rows.Close()
			return err
		}
		current[tag.id] = tag
		currentOrder = append(currentOrder, tag.id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	requested := make(map[string]tagAssignment, len(unique))
	for _, tagID := range unique {
		var tag tagAssignment
		if err := tx.QueryRowContext(ctx, `SELECT id,name,color,icon FROM mod_tags WHERE id=?`, tagID).Scan(&tag.id, &tag.name, &tag.color, &tag.icon); err != nil {
			return fmt.Errorf("unknown tag %q: %w", tagID, err)
		}
		requested[tagID] = tag
	}
	same := len(current) == len(requested)
	if same {
		for _, tagID := range unique {
			if _, exists := current[tagID]; !exists {
				same = false
				break
			}
		}
	}
	if same {
		return tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM mod_tag_entities WHERE entity_id=?`, entityID); err != nil {
		return err
	}
	for _, tagID := range unique {
		if _, err := tx.ExecContext(ctx, `INSERT INTO mod_tag_entities(tag_id,entity_id,created_at) VALUES(?,?,?)`, tagID, entityID, nowUTC()); err != nil {
			return err
		}
	}
	for _, tagID := range currentOrder {
		if _, exists := requested[tagID]; exists {
			continue
		}
		tag := current[tagID]
		if err := appendEventTx(ctx, tx, entityID, "tag_removed", map[string]any{
			"tagId": tag.id, "tagName": tag.name, "color": tag.color, "icon": tag.icon,
		}); err != nil {
			return err
		}
	}
	for _, tagID := range unique {
		if _, exists := current[tagID]; exists {
			continue
		}
		tag := requested[tagID]
		if err := appendEventTx(ctx, tx, entityID, "tag_added", map[string]any{
			"tagId": tag.id, "tagName": tag.name, "color": tag.color, "icon": tag.icon,
		}); err != nil {
			return err
		}
	}
	if err := s.refreshLibrarySearchEntryTx(ctx, tx, entityID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) attachLibraryItemTags(ctx context.Context, items []LibraryItem) error {
	if len(items) == 0 {
		return nil
	}
	byEntity := make(map[string]*LibraryItem, len(items))
	for index := range items {
		items[index].Tags = []ModTag{}
		byEntity[items[index].EntityID] = &items[index]
	}
	query := `SELECT te.entity_id,t.id,t.name,t.color,t.icon,t.origin FROM mod_tag_entities te JOIN mod_tags t ON t.id=te.tag_id`
	args := []any{}
	if len(items) == 1 {
		query += ` WHERE te.entity_id=?`
		args = append(args, items[0].EntityID)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var entityID string
		var tag ModTag
		if err := rows.Scan(&entityID, &tag.ID, &tag.Name, &tag.Color, &tag.Icon, &tag.Origin); err != nil {
			return err
		}
		if item := byEntity[entityID]; item != nil {
			item.Tags = append(item.Tags, tag)
		}
	}
	return rows.Err()
}
