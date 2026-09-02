package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	modkit "github.com/SignedAdam/beamworlds-modkit"
)

type LibraryFolder struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	ParentID string `json:"parentId"`
	ModCount int    `json:"modCount"`
}

type ModPreset struct {
	ID                     string `json:"id"`
	Name                   string `json:"name"`
	Description            string `json:"description"`
	ModCount               int    `json:"modCount"`
	DefaultForProfileCount int    `json:"defaultForProfileCount"`
}
type PresetDetail struct {
	Preset    ModPreset `json:"preset"`
	EntityIDs []string  `json:"entityIds"`
}

type ModProfile struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	DefaultPresetID string `json:"defaultPresetId"`
	PresetCount     int    `json:"presetCount"`
	ModCount        int    `json:"modCount"`
}

type OrganizationState struct {
	Folders  []LibraryFolder `json:"folders"`
	Tags     []ModTag        `json:"tags"`
	Presets  []ModPreset     `json:"presets"`
	Profiles []ModProfile    `json:"profiles"`
}

type ProfilePreset struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	ModCount    int    `json:"modCount"`
	Selected    bool   `json:"selected"`
	Default     bool   `json:"default"`
}

type ProfileMod struct {
	EntityID    string      `json:"entityId"`
	DisplayName string      `json:"displayName"`
	Kind        modkit.Kind `json:"kind"`
	ArchivePath string      `json:"archivePath"`
	SHA256      string      `json:"sha256"`
	SizeBytes   int64       `json:"sizeBytes"`
	PresetIDs   []string    `json:"presetIds"`
}

type ProfileDetail struct {
	Profile ModProfile      `json:"profile"`
	Presets []ProfilePreset `json:"presets"`
	Mods    []ProfileMod    `json:"mods"`
}

func (service *AppService) Organization() (OrganizationState, error) {
	return service.store.Organization(context.Background())
}

func (service *AppService) CreateLibraryFolder(name, parentID string) (OrganizationState, error) {
	if err := service.store.CreateLibraryFolder(context.Background(), name, parentID); err != nil {
		return OrganizationState{}, err
	}
	return service.Organization()
}

func (service *AppService) RenameLibraryFolder(folderID, name string) (OrganizationState, error) {
	if err := service.store.RenameLibraryFolder(context.Background(), folderID, name); err != nil {
		return OrganizationState{}, err
	}
	return service.Organization()
}

func (service *AppService) DeleteLibraryFolder(folderID string) (OrganizationState, error) {
	if err := service.store.DeleteLibraryFolder(context.Background(), folderID); err != nil {
		return OrganizationState{}, err
	}
	return service.Organization()
}

func (service *AppService) MoveLibraryItem(entityID, folderID string) error {
	return service.store.MoveLibraryItem(context.Background(), entityID, folderID)
}

func (service *AppService) CreatePreset(name, description string) (OrganizationState, error) {
	if err := service.store.CreatePreset(context.Background(), name, description); err != nil {
		return OrganizationState{}, err
	}
	return service.Organization()
}

func (service *AppService) UpdatePreset(presetID, name, description string) (OrganizationState, error) {
	if err := service.store.UpdatePreset(context.Background(), presetID, name, description); err != nil {
		return OrganizationState{}, err
	}
	return service.Organization()
}

func (service *AppService) DeletePreset(presetID string) (OrganizationState, error) {
	if err := service.store.DeletePreset(context.Background(), presetID); err != nil {
		return OrganizationState{}, err
	}
	return service.Organization()
}

func (service *AppService) SetPresetMod(presetID, entityID string, included bool) error {
	return service.store.SetPresetMod(context.Background(), presetID, entityID, included)
}
func (service *AppService) GetPreset(presetID string) (PresetDetail, error) {
	return service.store.PresetDetail(context.Background(), presetID)
}

func (service *AppService) CreateProfile(name string) (OrganizationState, error) {
	if err := service.store.CreateProfile(context.Background(), name); err != nil {
		return OrganizationState{}, err
	}
	return service.Organization()
}

func (service *AppService) RenameProfile(profileID, name string) (OrganizationState, error) {
	if err := service.store.RenameProfile(context.Background(), profileID, name); err != nil {
		return OrganizationState{}, err
	}
	return service.Organization()
}

func (service *AppService) DeleteProfile(profileID string) (OrganizationState, error) {
	if err := service.store.DeleteProfile(context.Background(), profileID); err != nil {
		return OrganizationState{}, err
	}
	return service.Organization()
}

func (service *AppService) SetProfilePreset(profileID, presetID string, selected bool) error {
	return service.store.SetProfilePreset(context.Background(), profileID, presetID, selected)
}

func (service *AppService) SetProfileMod(profileID, entityID string, included bool) error {
	return service.store.SetProfileMod(context.Background(), profileID, entityID, included)
}

func (service *AppService) GetProfile(profileID string) (ProfileDetail, error) {
	return service.store.ProfileDetail(context.Background(), profileID)
}

func cleanOrganizationName(value, label string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 80 || strings.ContainsAny(value, "\r\n\t") {
		return "", fmt.Errorf("%s must contain 1 to 80 characters on one line", label)
	}
	return value, nil
}

func (s *Store) Organization(ctx context.Context) (OrganizationState, error) {
	folders, err := s.listLibraryFolders(ctx)
	if err != nil {
		return OrganizationState{}, err
	}
	tags, err := s.listModTags(ctx)
	if err != nil {
		return OrganizationState{}, err
	}
	presets, err := s.listPresets(ctx)
	if err != nil {
		return OrganizationState{}, err
	}
	profiles, err := s.listProfiles(ctx)
	if err != nil {
		return OrganizationState{}, err
	}
	return OrganizationState{Folders: folders, Tags: tags, Presets: presets, Profiles: profiles}, nil
}

func (s *Store) listLibraryFolders(ctx context.Context) ([]LibraryFolder, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT f.id,f.name,COALESCE(f.parent_id,''),COUNT(fe.entity_id) FROM library_folders f LEFT JOIN library_folder_entities fe ON fe.folder_id=f.id GROUP BY f.id ORDER BY f.position,f.name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []LibraryFolder{}
	for rows.Next() {
		var folder LibraryFolder
		if err := rows.Scan(&folder.ID, &folder.Name, &folder.ParentID, &folder.ModCount); err != nil {
			return nil, err
		}
		result = append(result, folder)
	}
	return result, rows.Err()
}

func (s *Store) CreateLibraryFolder(ctx context.Context, name, parentID string) error {
	name, err := cleanOrganizationName(name, "folder name")
	if err != nil {
		return err
	}
	id, err := modkit.NewID()
	if err != nil {
		return err
	}
	var parent any
	parentID = strings.TrimSpace(parentID)
	if parentID != "" {
		if err := s.db.QueryRowContext(ctx, `SELECT id FROM library_folders WHERE id=?`, parentID).Scan(&parentID); err != nil {
			return err
		}
		parent = parentID
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO library_folders(id,name,parent_id,position,created_at,updated_at) VALUES(?,?,?,(SELECT COALESCE(MAX(position),-1)+1 FROM library_folders),?,?)`, id, name, parent, nowUTC(), nowUTC())
	return err
}

func (s *Store) RenameLibraryFolder(ctx context.Context, folderID, name string) error {
	name, err := cleanOrganizationName(name, "folder name")
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE library_folders SET name=?,updated_at=? WHERE id=?`, name, nowUTC(), strings.TrimSpace(folderID))
	return requireChanged(result, err, "library folder")
}

func (s *Store) DeleteLibraryFolder(ctx context.Context, folderID string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM library_folders WHERE id=?`, strings.TrimSpace(folderID))
	return requireChanged(result, err, "library folder")
}

func (s *Store) MoveLibraryItem(ctx context.Context, entityID, folderID string) error {
	entityID = strings.TrimSpace(entityID)
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM entities WHERE id=?`, entityID).Scan(&entityID); err != nil {
		return err
	}
	folderID = strings.TrimSpace(folderID)
	if folderID == "" {
		_, err := s.db.ExecContext(ctx, `DELETE FROM library_folder_entities WHERE entity_id=?`, entityID)
		return err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM library_folders WHERE id=?`, folderID).Scan(&folderID); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO library_folder_entities(entity_id,folder_id,position) VALUES(?,?,0) ON CONFLICT(entity_id) DO UPDATE SET folder_id=excluded.folder_id,position=excluded.position`, entityID, folderID)
	return err
}

func (s *Store) listPresets(ctx context.Context) ([]ModPreset, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT p.id,p.name,p.description,COUNT(DISTINCT pe.entity_id),COUNT(DISTINCT mp.id) FROM mod_presets p LEFT JOIN mod_preset_entities pe ON pe.preset_id=p.id LEFT JOIN mod_profiles mp ON mp.default_preset_id=p.id GROUP BY p.id ORDER BY CASE WHEN COUNT(DISTINCT mp.id)>0 THEN 1 ELSE 0 END,p.name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []ModPreset{}
	for rows.Next() {
		var preset ModPreset
		if err := rows.Scan(&preset.ID, &preset.Name, &preset.Description, &preset.ModCount, &preset.DefaultForProfileCount); err != nil {
			return nil, err
		}
		result = append(result, preset)
	}
	return result, rows.Err()
}

func (s *Store) CreatePreset(ctx context.Context, name, description string) error {
	name, err := cleanOrganizationName(name, "preset name")
	if err != nil {
		return err
	}
	if len(description) > 500 {
		return errors.New("preset description exceeds 500 characters")
	}
	id, err := modkit.NewID()
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO mod_presets(id,name,description,created_at,updated_at) VALUES(?,?,?,?,?)`, id, name, strings.TrimSpace(description), nowUTC(), nowUTC())
	return err
}

func (s *Store) UpdatePreset(ctx context.Context, presetID, name, description string) error {
	name, err := cleanOrganizationName(name, "preset name")
	if err != nil {
		return err
	}
	if len(description) > 500 {
		return errors.New("preset description exceeds 500 characters")
	}
	var defaults int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM mod_profiles WHERE default_preset_id=?`, presetID).Scan(&defaults); err != nil {
		return err
	}
	if defaults > 0 {
		return errors.New("a profile's default preset is renamed with its profile")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE mod_presets SET name=?,description=?,updated_at=? WHERE id=?`, name, strings.TrimSpace(description), nowUTC(), presetID)
	return requireChanged(result, err, "preset")
}

func (s *Store) DeletePreset(ctx context.Context, presetID string) error {
	var defaults int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM mod_profiles WHERE default_preset_id=?`, presetID).Scan(&defaults); err != nil {
		return err
	}
	if defaults > 0 {
		return errors.New("a profile's default preset cannot be deleted")
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM mod_presets WHERE id=?`, presetID)
	return requireChanged(result, err, "preset")
}

func (s *Store) SetPresetMod(ctx context.Context, presetID, entityID string, included bool) error {
	var exists string
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM mod_presets WHERE id=?`, presetID).Scan(&exists); err != nil {
		return err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM entities WHERE id=?`, entityID).Scan(&exists); err != nil {
		return err
	}
	if !included {
		_, err := s.db.ExecContext(ctx, `DELETE FROM mod_preset_entities WHERE preset_id=? AND entity_id=?`, presetID, entityID)
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO mod_preset_entities(preset_id,entity_id,position) VALUES(?,?,0) ON CONFLICT(preset_id,entity_id) DO NOTHING`, presetID, entityID)
	return err
}
func (s *Store) PresetDetail(ctx context.Context, presetID string) (PresetDetail, error) {
	var detail PresetDetail
	if err := s.db.QueryRowContext(ctx, `SELECT p.id,p.name,p.description,COUNT(DISTINCT pe.entity_id),COUNT(DISTINCT mp.id) FROM mod_presets p LEFT JOIN mod_preset_entities pe ON pe.preset_id=p.id LEFT JOIN mod_profiles mp ON mp.default_preset_id=p.id WHERE p.id=? GROUP BY p.id`, presetID).Scan(&detail.Preset.ID, &detail.Preset.Name, &detail.Preset.Description, &detail.Preset.ModCount, &detail.Preset.DefaultForProfileCount); err != nil {
		return PresetDetail{}, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT entity_id FROM mod_preset_entities WHERE preset_id=? ORDER BY position,entity_id`, presetID)
	if err != nil {
		return PresetDetail{}, err
	}
	defer rows.Close()
	detail.EntityIDs = []string{}
	for rows.Next() {
		var entityID string
		if err := rows.Scan(&entityID); err != nil {
			return PresetDetail{}, err
		}
		detail.EntityIDs = append(detail.EntityIDs, entityID)
	}
	return detail, rows.Err()
}

func (s *Store) listProfiles(ctx context.Context) ([]ModProfile, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT p.id,p.name,p.default_preset_id,COUNT(DISTINCT CASE WHEN pp.preset_id<>p.default_preset_id THEN pp.preset_id END),COUNT(DISTINCT pe.entity_id) FROM mod_profiles p LEFT JOIN mod_profile_presets pp ON pp.profile_id=p.id LEFT JOIN mod_preset_entities pe ON pe.preset_id=pp.preset_id GROUP BY p.id ORDER BY p.name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []ModProfile{}
	for rows.Next() {
		var profile ModProfile
		if err := rows.Scan(&profile.ID, &profile.Name, &profile.DefaultPresetID, &profile.PresetCount, &profile.ModCount); err != nil {
			return nil, err
		}
		result = append(result, profile)
	}
	return result, rows.Err()
}

func (s *Store) CreateProfile(ctx context.Context, name string) error {
	name, err := cleanOrganizationName(name, "profile name")
	if err != nil {
		return err
	}
	profileID, err := modkit.NewID()
	if err != nil {
		return err
	}
	presetID, err := modkit.NewID()
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := nowUTC()
	if _, err := tx.ExecContext(ctx, `INSERT INTO mod_presets(id,name,description,created_at,updated_at) VALUES(?,?,?, ?,?)`, presetID, name+" — Default", "Mods selected directly for this profile.", now, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO mod_profiles(id,name,default_preset_id,created_at,updated_at) VALUES(?,?,?,?,?)`, profileID, name, presetID, now, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO mod_profile_presets(profile_id,preset_id,position) VALUES(?,?,0)`, profileID, presetID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RenameProfile(ctx context.Context, profileID, name string) error {
	name, err := cleanOrganizationName(name, "profile name")
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var defaultPresetID string
	if err := tx.QueryRowContext(ctx, `SELECT default_preset_id FROM mod_profiles WHERE id=?`, profileID).Scan(&defaultPresetID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE mod_profiles SET name=?,updated_at=? WHERE id=?`, name, nowUTC(), profileID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE mod_presets SET name=?,updated_at=? WHERE id=?`, name+" — Default", nowUTC(), defaultPresetID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DeleteProfile(ctx context.Context, profileID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var presetID string
	if err := tx.QueryRowContext(ctx, `SELECT default_preset_id FROM mod_profiles WHERE id=?`, profileID).Scan(&presetID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM mod_profiles WHERE id=?`, profileID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM mod_presets WHERE id=?`, presetID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SetProfilePreset(ctx context.Context, profileID, presetID string, selected bool) error {
	var defaultPresetID string
	if err := s.db.QueryRowContext(ctx, `SELECT default_preset_id FROM mod_profiles WHERE id=?`, profileID).Scan(&defaultPresetID); err != nil {
		return err
	}
	if presetID == defaultPresetID && !selected {
		return errors.New("the default preset is always selected")
	}
	var exists string
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM mod_presets WHERE id=?`, presetID).Scan(&exists); err != nil {
		return err
	}
	if !selected {
		_, err := s.db.ExecContext(ctx, `DELETE FROM mod_profile_presets WHERE profile_id=? AND preset_id=?`, profileID, presetID)
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO mod_profile_presets(profile_id,preset_id,position) VALUES(?,?,0) ON CONFLICT(profile_id,preset_id) DO NOTHING`, profileID, presetID)
	return err
}

func (s *Store) SetProfileMod(ctx context.Context, profileID, entityID string, included bool) error {
	var presetID string
	if err := s.db.QueryRowContext(ctx, `SELECT default_preset_id FROM mod_profiles WHERE id=?`, profileID).Scan(&presetID); err != nil {
		return err
	}
	return s.SetPresetMod(ctx, presetID, entityID, included)
}

func (s *Store) ProfileDetail(ctx context.Context, profileID string) (ProfileDetail, error) {
	profiles, err := s.listProfiles(ctx)
	if err != nil {
		return ProfileDetail{}, err
	}
	var profile ModProfile
	for _, candidate := range profiles {
		if candidate.ID == profileID {
			profile = candidate
			break
		}
	}
	if profile.ID == "" {
		return ProfileDetail{}, sql.ErrNoRows
	}
	rows, err := s.db.QueryContext(ctx, `SELECT p.id,p.name,p.description,COUNT(DISTINCT pe.entity_id),EXISTS(SELECT 1 FROM mod_profile_presets pp WHERE pp.profile_id=? AND pp.preset_id=p.id),p.id=? FROM mod_presets p LEFT JOIN mod_preset_entities pe ON pe.preset_id=p.id GROUP BY p.id ORDER BY CASE WHEN p.id=? THEN 0 ELSE 1 END,p.name COLLATE NOCASE`, profileID, profile.DefaultPresetID, profile.DefaultPresetID)
	if err != nil {
		return ProfileDetail{}, err
	}
	presets := []ProfilePreset{}
	for rows.Next() {
		var preset ProfilePreset
		if err := rows.Scan(&preset.ID, &preset.Name, &preset.Description, &preset.ModCount, &preset.Selected, &preset.Default); err != nil {
			rows.Close()
			return ProfileDetail{}, err
		}
		presets = append(presets, preset)
	}
	if err := rows.Close(); err != nil {
		return ProfileDetail{}, err
	}
	modRows, err := s.db.QueryContext(ctx, `SELECT e.id,e.display_name,e.kind,COALESCE(l.path,''),COALESCE(a.sha256,''),COALESCE(l.size_bytes,0),GROUP_CONCAT(DISTINCT pe.preset_id) FROM mod_profile_presets pp JOIN mod_preset_entities pe ON pe.preset_id=pp.preset_id JOIN entities e ON e.id=pe.entity_id LEFT JOIN archive_links l ON l.id=(SELECT l2.id FROM archive_links l2 WHERE l2.entity_id=e.id ORDER BY l2.active DESC,l2.last_seen_at DESC LIMIT 1) LEFT JOIN artifacts a ON a.id=l.artifact_id WHERE pp.profile_id=? GROUP BY e.id ORDER BY e.display_name COLLATE NOCASE`, profileID)
	if err != nil {
		return ProfileDetail{}, err
	}
	defer modRows.Close()
	mods := []ProfileMod{}
	for modRows.Next() {
		var mod ProfileMod
		var kind, presetIDs string
		if err := modRows.Scan(&mod.EntityID, &mod.DisplayName, &kind, &mod.ArchivePath, &mod.SHA256, &mod.SizeBytes, &presetIDs); err != nil {
			return ProfileDetail{}, err
		}
		mod.Kind = modkit.Kind(kind)
		if presetIDs != "" {
			mod.PresetIDs = strings.Split(presetIDs, ",")
		}
		mods = append(mods, mod)
	}
	return ProfileDetail{Profile: profile, Presets: presets, Mods: mods}, modRows.Err()
}

func requireChanged(result sql.Result, err error, label string) error {
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return fmt.Errorf("%s was not found", label)
	}
	return nil
}
