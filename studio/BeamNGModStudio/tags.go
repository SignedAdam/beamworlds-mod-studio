package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	modkit "github.com/SignedAdam/beamworlds-modkit"
)

const exampleTagSeedKey = "library_example_tags_seeded_v1"

var exampleModTagNames = []string{
	"Car",
	"Motorbike",
	"UI",
	"Gameplay Overhaul",
	"Vehicle Effects",
	"Boat",
	"Props",
	"Airplane",
	"Helicopter",
	"Mission",
}

type ModTag struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	ModCount int    `json:"modCount"`
}

func (service *AppService) CreateModTag(name string) (OrganizationState, error) {
	if err := service.store.CreateModTag(context.Background(), name); err != nil {
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
	if err := service.store.DeleteModTag(context.Background(), tagID); err != nil {
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

func (s *Store) ensureExampleModTags(ctx context.Context) error {
	var seeded string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, exampleTagSeedKey).Scan(&seeded)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, name := range exampleModTagNames {
		if _, err := tx.ExecContext(ctx, `INSERT INTO mod_tags(id,name,created_at,updated_at) VALUES(?,?,?,?) ON CONFLICT(name) DO NOTHING`, "example-"+tagIDPart(name), name, nowUTC(), nowUTC()); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, exampleTagSeedKey, "1"); err != nil {
		return err
	}
	return tx.Commit()
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
	rows, err := s.db.QueryContext(ctx, `SELECT t.id,t.name,COUNT(te.entity_id) FROM mod_tags t LEFT JOIN mod_tag_entities te ON te.tag_id=t.id GROUP BY t.id ORDER BY t.name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []ModTag{}
	for rows.Next() {
		var tag ModTag
		if err := rows.Scan(&tag.ID, &tag.Name, &tag.ModCount); err != nil {
			return nil, err
		}
		result = append(result, tag)
	}
	return result, rows.Err()
}

func (s *Store) CreateModTag(ctx context.Context, name string) error {
	name, err := cleanOrganizationName(name, "tag name")
	if err != nil {
		return err
	}
	id, err := modkit.NewID()
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO mod_tags(id,name,created_at,updated_at) VALUES(?,?,?,?)`, id, name, nowUTC(), nowUTC())
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "unique") {
		return fmt.Errorf("tag %q already exists", name)
	}
	return err
}

func (s *Store) RenameModTag(ctx context.Context, tagID, name string) error {
	name, err := cleanOrganizationName(name, "tag name")
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE mod_tags SET name=?,updated_at=? WHERE id=?`, name, nowUTC(), strings.TrimSpace(tagID))
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "unique") {
		return fmt.Errorf("tag %q already exists", name)
	}
	return requireChanged(result, err, "tag")
}

func (s *Store) DeleteModTag(ctx context.Context, tagID string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM mod_tags WHERE id=?`, strings.TrimSpace(tagID))
	return requireChanged(result, err, "tag")
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
	var exists string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM entities WHERE id=?`, entityID).Scan(&exists); err != nil {
		return err
	}
	for _, tagID := range unique {
		if err := tx.QueryRowContext(ctx, `SELECT id FROM mod_tags WHERE id=?`, tagID).Scan(&exists); err != nil {
			return fmt.Errorf("unknown tag %q: %w", tagID, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM mod_tag_entities WHERE entity_id=?`, entityID); err != nil {
		return err
	}
	for _, tagID := range unique {
		if _, err := tx.ExecContext(ctx, `INSERT INTO mod_tag_entities(tag_id,entity_id,created_at) VALUES(?,?,?)`, tagID, entityID, nowUTC()); err != nil {
			return err
		}
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
	query := `SELECT te.entity_id,t.id,t.name FROM mod_tag_entities te JOIN mod_tags t ON t.id=te.tag_id`
	args := []any{}
	if len(items) == 1 {
		query += ` WHERE te.entity_id=?`
		args = append(args, items[0].EntityID)
	}
	query += ` ORDER BY t.name COLLATE NOCASE`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var entityID string
		var tag ModTag
		if err := rows.Scan(&entityID, &tag.ID, &tag.Name); err != nil {
			return err
		}
		if item := byEntity[entityID]; item != nil {
			item.Tags = append(item.Tags, tag)
		}
	}
	return rows.Err()
}
