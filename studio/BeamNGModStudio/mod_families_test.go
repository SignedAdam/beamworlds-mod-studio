package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	modkit "github.com/SignedAdam/beamworlds-modkit"
)

func TestModFamiliesRepoGroupingRanksHigherVersion(t *testing.T) {
	service := newTestAppService(t)
	root := filepath.Join(service.config.DataDir, "library")
	archives := []ScanArchive{
		modFamilyScanArchive(t, root, "repo-old.zip", "Road Runner", "Garage Team", "1.9", "repo-42", "sha-old", "fingerprint-old", 100, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), 0),
		modFamilyScanArchive(t, root, "repo-new.zip", "Road Runner", "Garage Team", "1.10 Repo", "repo-42", "sha-new", "fingerprint-new", 200, time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), 1),
	}
	applyLibraryArchives(t, service.store, root, archives)

	families, err := service.ModFamilies()
	if err != nil {
		t.Fatal(err)
	}
	if len(families) != 1 {
		t.Fatalf("families = %#v, want one repo family", families)
	}
	family := families[0]
	if family.ID != "repo:repo-42" || family.Confidence != "repo" || family.ResourceID != "repo-42" {
		t.Fatalf("repo family identity = %#v", family)
	}
	if family.Kind != "versions" || family.ReclaimableBytes != 100 || len(family.Members) != 2 {
		t.Fatalf("repo family summary = %#v", family)
	}
	if family.Members[0].Version != "1.10 Repo" || !family.Members[0].Keeper || family.Members[0].KeeperReason != "version 1.10 Repo" {
		t.Fatalf("keeper = %#v", family.Members)
	}
	if family.Members[1].Keeper || family.Members[1].Version != "1.9" {
		t.Fatalf("members were not ordered keeper-first: %#v", family.Members)
	}
}

func TestModFamiliesRepoGroupingAcceptsNumericResourceID(t *testing.T) {
	service := newTestAppService(t)
	root := filepath.Join(service.config.DataDir, "library")
	archives := []ScanArchive{
		modFamilyScanArchive(t, root, "numeric-one.zip", "Autobahn 57k", "Grille", "1.2", "", "numeric-sha-one", "numeric-fingerprint-one", 100, time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), 0),
		modFamilyScanArchive(t, root, "numeric-two.zip", "Autobahn 57k", "Grille", "1.6", "", "numeric-sha-two", "numeric-fingerprint-two", 100, time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC), 0),
	}
	for index := range archives {
		archives[index].Manifest.MetadataDocuments = []modkit.MetadataDocument{{
			Path: "mod_info/info.json",
			Data: map[string]any{"Resource_ID": json.Number("33295")},
		}}
	}
	applyLibraryArchives(t, service.store, root, archives)

	families, err := service.ModFamilies()
	if err != nil {
		t.Fatal(err)
	}
	if len(families) != 1 || families[0].Confidence != "repo" || families[0].ID != "repo:33295" {
		t.Fatalf("families = %#v, want numeric repo family", families)
	}
}

func TestModFamiliesMetadataGroupingWithoutResourceID(t *testing.T) {
	service := newTestAppService(t)
	root := filepath.Join(service.config.DataDir, "library")
	archives := []ScanArchive{
		modFamilyScanArchive(t, root, "metadata-one.zip", "  Desert   Racer ", "  Mod   Maker ", "", "", "sha-one", "metadata-one", 100, time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), 0),
		modFamilyScanArchive(t, root, "metadata-two.zip", "desert racer", "mod maker", "", "", "sha-two", "metadata-two", 120, time.Date(2026, 2, 2, 0, 0, 0, 0, time.UTC), 0),
	}
	applyLibraryArchives(t, service.store, root, archives)

	families, err := service.ModFamilies()
	if err != nil {
		t.Fatal(err)
	}
	if len(families) != 1 {
		t.Fatalf("families = %#v, want one metadata family", families)
	}
	family := families[0]
	if family.Confidence != "metadata" || family.ResourceID != "" || !strings.HasPrefix(family.ID, "meta:") {
		t.Fatalf("metadata family identity = %#v", family)
	}
	if len(family.Members) != 2 || family.Title != "desert racer" && family.Title != "  Desert   Racer " {
		t.Fatalf("metadata family members/title = %#v", family)
	}
}

func TestModFamiliesSameTitleDifferentAuthorsAreNotGrouped(t *testing.T) {
	service := newTestAppService(t)
	root := filepath.Join(service.config.DataDir, "library")
	archives := []ScanArchive{
		modFamilyScanArchive(t, root, "author-one.zip", "Shared Title", "Author One", "", "", "sha-one", "author-one", 100, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), 0),
		modFamilyScanArchive(t, root, "author-two.zip", "Shared Title", "Author Two", "", "", "sha-two", "author-two", 100, time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC), 0),
	}
	applyLibraryArchives(t, service.store, root, archives)

	families, err := service.ModFamilies()
	if err != nil {
		t.Fatal(err)
	}
	if len(families) != 0 {
		t.Fatalf("families = %#v, want no family for different authors", families)
	}
}

func TestModFamiliesSameTitleWithEmptyAuthorIsNotGrouped(t *testing.T) {
	service := newTestAppService(t)
	root := filepath.Join(service.config.DataDir, "library")
	archives := []ScanArchive{
		modFamilyScanArchive(t, root, "empty-author.zip", "Shared Title", "", "", "", "sha-one", "empty-author", 100, time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), 0),
		modFamilyScanArchive(t, root, "named-author.zip", "Shared Title", "Author", "", "", "sha-two", "named-author", 100, time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC), 0),
	}
	applyLibraryArchives(t, service.store, root, archives)

	families, err := service.ModFamilies()
	if err != nil {
		t.Fatal(err)
	}
	if len(families) != 0 {
		t.Fatalf("families = %#v, want no family when an author is empty", families)
	}
}

func TestModFamiliesReportCopiesAndVersions(t *testing.T) {
	service := newTestAppService(t)
	root := filepath.Join(service.config.DataDir, "library")
	archives := []ScanArchive{
		modFamilyScanArchive(t, root, "copy-one.zip", "Copy Family", "Author", "1.0", "repo-copies", "same-sha", "copy-fingerprint", 100, time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), 0),
		modFamilyScanArchive(t, root, "copy-two.zip", "Copy Family", "Author", "1.0", "repo-copies", "same-sha", "copy-fingerprint", 100, time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC), 0),
		modFamilyScanArchive(t, root, "version-one.zip", "Version Family", "Author", "1.0", "repo-versions", "version-sha-one", "version-fingerprint-one", 100, time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), 0),
		modFamilyScanArchive(t, root, "version-two.zip", "Version Family", "Author", "1.1", "repo-versions", "version-sha-two", "version-fingerprint-two", 100, time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC), 0),
		// A real library rarely has the full hash: 20 of 453 artifacts carried
		// one, and no manifest did. Identical copies must still read as copies
		// from the fingerprint alone.
		modFamilyScanArchive(t, root, "hashless-one.zip", "Hashless Family", "Author", "2.0", "repo-hashless", "", "hashless-fingerprint", 100, time.Date(2026, 5, 3, 0, 0, 0, 0, time.UTC), 0),
		modFamilyScanArchive(t, root, "hashless-two.zip", "Hashless Family", "Author", "2.0", "repo-hashless", "", "hashless-fingerprint", 100, time.Date(2026, 5, 4, 0, 0, 0, 0, time.UTC), 0),
	}
	applyLibraryArchives(t, service.store, root, archives)

	families, err := service.ModFamilies()
	if err != nil {
		t.Fatal(err)
	}
	if len(families) != 3 {
		t.Fatalf("families = %#v, want copy, version, and hashless families", families)
	}
	byID := make(map[string]ModFamily, len(families))
	for _, family := range families {
		byID[family.ID] = family
	}
	if got := byID["repo:repo-copies"].Kind; got != "copies" {
		t.Fatalf("copy family kind = %q, want copies", got)
	}
	if got := byID["repo:repo-versions"].Kind; got != "versions" {
		t.Fatalf("version family kind = %q, want versions", got)
	}
	if got := byID["repo:repo-hashless"].Kind; got != "copies" {
		t.Fatalf("hashless family kind = %q, want copies from the fingerprint alone", got)
	}
	if got := byID["repo:repo-hashless"].Members[0].KeeperReason; got != "newest file" {
		t.Fatalf("hashless keeper reason = %q, want newest file", got)
	}
}

func TestModFamiliesDismissalSurvivesRescanAndMembershipChange(t *testing.T) {
	service := newTestAppService(t)
	root := filepath.Join(service.config.DataDir, "library")
	first := []ScanArchive{
		modFamilyScanArchive(t, root, "dismiss-one.zip", "Dismiss Family", "Author", "1.0", "repo-dismiss", "dismiss-sha-one", "dismiss-fingerprint-one", 100, time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), 0),
		modFamilyScanArchive(t, root, "dismiss-two.zip", "Dismiss Family", "Author", "1.1", "repo-dismiss", "dismiss-sha-two", "dismiss-fingerprint-two", 100, time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC), 0),
	}
	applyLibraryArchives(t, service.store, root, first)
	families, err := service.ModFamilies()
	if err != nil {
		t.Fatal(err)
	}
	if len(families) != 1 {
		t.Fatalf("initial families = %#v", families)
	}
	familyID := families[0].ID
	dismissed, err := service.DismissModFamily(familyID)
	if err != nil {
		t.Fatal(err)
	}
	if dismissed == nil || len(dismissed) != 0 {
		t.Fatalf("dismissed families = %#v, want empty non-nil list", dismissed)
	}
	redismissed, err := service.DismissModFamily(familyID)
	if err != nil {
		t.Fatal(err)
	}
	if redismissed == nil || len(redismissed) != 0 {
		t.Fatalf("re-dismissed families = %#v, want empty non-nil list", redismissed)
	}

	applyLibraryArchives(t, service.store, root, first)
	rescanHidden, err := service.ModFamilies()
	if err != nil {
		t.Fatal(err)
	}
	if len(rescanHidden) != 0 {
		t.Fatalf("families after unchanged rescan = %#v, want hidden", rescanHidden)
	}

	withThird := append(append([]ScanArchive{}, first...), modFamilyScanArchive(t, root, "dismiss-three.zip", "Dismiss Family", "Author", "1.2", "repo-dismiss", "dismiss-sha-three", "dismiss-fingerprint-three", 100, time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC), 0))
	applyLibraryArchives(t, service.store, root, withThird)
	reappeared, err := service.ModFamilies()
	if err != nil {
		t.Fatal(err)
	}
	if len(reappeared) != 1 || len(reappeared[0].Members) != 3 {
		t.Fatalf("families after third member = %#v, want reappeared three-member family", reappeared)
	}
}

func TestModFamiliesEmptyLibraryReturnsNonNilSlice(t *testing.T) {
	service := newTestAppService(t)
	families, err := service.ModFamilies()
	if err != nil {
		t.Fatal(err)
	}
	if families == nil || len(families) != 0 {
		t.Fatalf("families = %#v, want empty non-nil slice", families)
	}
}

func TestModFamiliesFileFamilyRanksNewerArchive(t *testing.T) {
	service := newTestAppService(t)
	root := filepath.Join(service.config.DataDir, "library")
	archives := []ScanArchive{
		modFamilyScanArchive(t, root, filepath.Join("old", "same-name.zip"), "Copies Mod", "Archive Author", "1.0", "", "copies-sha", "copies-fingerprint", 100, time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), 0),
		modFamilyScanArchive(t, root, filepath.Join("new", "same-name.zip"), "Copies Mod", "Archive Author", "1.0", "", "copies-sha", "copies-fingerprint", 200, time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC), 0),
	}
	items := applyLibraryArchives(t, service.store, root, archives)
	if len(items) != 1 {
		t.Fatalf("items = %#v, want one entity with two links", items)
	}

	families, err := service.ModFamilies()
	if err != nil {
		t.Fatal(err)
	}
	if len(families) != 1 {
		t.Fatalf("families = %#v, want one files family", families)
	}
	family := families[0]
	if family.ID != "files:"+items[0].EntityID || family.Confidence != "identical" || family.Kind != "copies" {
		t.Fatalf("files family identity = %#v", family)
	}
	if family.Title != "Copies Mod" || family.Author != "Archive Author" || len(family.Members) != 2 || family.ReclaimableBytes != 100 {
		t.Fatalf("files family summary = %#v", family)
	}
	if family.Members[0].EntityID != family.Members[1].EntityID ||
		family.Members[0].LinkID == "" || family.Members[1].LinkID == "" ||
		family.Members[0].LinkID == family.Members[1].LinkID {
		t.Fatalf("files family link identity = %#v", family.Members)
	}
	if !family.Members[0].Keeper || family.Members[0].ArchivePath != archives[1].ArchivePath || family.Members[0].KeeperReason != "newest file" {
		t.Fatalf("files family keeper = %#v", family.Members)
	}
	if family.Members[1].Keeper || family.Members[1].ArchivePath != archives[0].ArchivePath {
		t.Fatalf("files family non-keeper = %#v", family.Members)
	}
}

func TestModFamiliesFilesAndMetadataCanShareEntity(t *testing.T) {
	service := newTestAppService(t)
	root := filepath.Join(service.config.DataDir, "library")
	archives := []ScanArchive{
		modFamilyScanArchive(t, root, filepath.Join("one", "same-name.zip"), "Overlap Mod", "Overlap Author", "", "", "overlap-sha", "overlap-fingerprint", 100, time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC), 0),
		modFamilyScanArchive(t, root, filepath.Join("two", "same-name.zip"), "Overlap Mod", "Overlap Author", "", "", "overlap-sha", "overlap-fingerprint", 120, time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC), 0),
		modFamilyScanArchive(t, root, filepath.Join("three", "different-name.zip"), "Overlap Mod", "Overlap Author", "", "", "separate-sha", "separate-fingerprint", 140, time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC), 0),
	}
	items := applyLibraryArchives(t, service.store, root, archives)
	if len(items) != 2 {
		t.Fatalf("items = %#v, want two entities", items)
	}
	var filesEntityID string
	for _, item := range items {
		if strings.HasSuffix(item.ArchivePath, filepath.Join("two", "same-name.zip")) {
			filesEntityID = item.EntityID
			break
		}
	}
	if filesEntityID == "" {
		t.Fatalf("could not identify the two-link entity from %#v", items)
	}

	families, err := service.ModFamilies()
	if err != nil {
		t.Fatal(err)
	}
	if len(families) != 2 {
		t.Fatalf("families = %#v, want files and metadata families", families)
	}
	var filesFamily, metadataFamily *ModFamily
	for index := range families {
		switch families[index].Confidence {
		case "identical":
			filesFamily = &families[index]
		case "metadata":
			metadataFamily = &families[index]
		}
	}
	if filesFamily == nil || metadataFamily == nil {
		t.Fatalf("families = %#v, want identical and metadata families", families)
	}
	if len(filesFamily.Members) != 2 || filesFamily.Members[0].EntityID != filesEntityID || filesFamily.Members[1].EntityID != filesEntityID {
		t.Fatalf("files family members = %#v", filesFamily.Members)
	}
	foundInMetadata := false
	for _, member := range metadataFamily.Members {
		if member.EntityID == filesEntityID {
			foundInMetadata = true
			break
		}
	}
	if !foundInMetadata {
		t.Fatalf("metadata family omitted files entity: %#v", metadataFamily.Members)
	}
}

func TestModFamiliesFileDismissalReappearsWhenLinkAdded(t *testing.T) {
	service := newTestAppService(t)
	root := filepath.Join(service.config.DataDir, "library")
	first := []ScanArchive{
		modFamilyScanArchive(t, root, filepath.Join("one", "same-name.zip"), "Dismiss Copies", "Archive Author", "", "", "dismiss-copies-sha", "dismiss-copies-fingerprint", 100, time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC), 0),
		modFamilyScanArchive(t, root, filepath.Join("two", "same-name.zip"), "Dismiss Copies", "Archive Author", "", "", "dismiss-copies-sha", "dismiss-copies-fingerprint", 100, time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC), 0),
	}
	applyLibraryArchives(t, service.store, root, first)
	families, err := service.ModFamilies()
	if err != nil {
		t.Fatal(err)
	}
	if len(families) != 1 || !strings.HasPrefix(families[0].ID, "files:") {
		t.Fatalf("initial families = %#v", families)
	}
	familyID := families[0].ID
	dismissed, err := service.DismissModFamily(familyID)
	if err != nil {
		t.Fatal(err)
	}
	if dismissed == nil || len(dismissed) != 0 {
		t.Fatalf("dismissed families = %#v, want empty non-nil list", dismissed)
	}

	applyLibraryArchives(t, service.store, root, first)
	hidden, err := service.ModFamilies()
	if err != nil {
		t.Fatal(err)
	}
	if hidden == nil || len(hidden) != 0 {
		t.Fatalf("families after unchanged rescan = %#v, want hidden", hidden)
	}

	withThird := append(append([]ScanArchive{}, first...), modFamilyScanArchive(t, root, filepath.Join("three", "same-name.zip"), "Dismiss Copies", "Archive Author", "", "", "dismiss-copies-sha", "dismiss-copies-fingerprint", 100, time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC), 0))
	applyLibraryArchives(t, service.store, root, withThird)
	reappeared, err := service.ModFamilies()
	if err != nil {
		t.Fatal(err)
	}
	if len(reappeared) != 1 || reappeared[0].ID != familyID || len(reappeared[0].Members) != 3 {
		t.Fatalf("families after third link = %#v, want reappeared three-link family", reappeared)
	}
}

func familyArchiveLinks(t *testing.T, service *AppService, entityID string) []ArchiveLink {
	t.Helper()
	detail, err := service.store.GetEntityDetail(context.Background(), entityID)
	if err != nil {
		t.Fatal(err)
	}
	links := make([]ArchiveLink, 0, len(detail.Links))
	for _, link := range detail.Links {
		if link.Linked {
			links = append(links, link)
		}
	}
	return links
}

func modFamilyScanArchive(t *testing.T, root, filename, title, author, version, resourceID, sha256Value, fingerprint string, sizeBytes int64, modified time.Time, resourceDocumentIndex int) ScanArchive {
	t.Helper()
	path := filepath.Join(root, filename)
	if err := writeFamilyFixtureArchive(path, sizeBytes); err != nil {
		t.Fatal(err)
	}
	metadataDocuments := []modkit.MetadataDocument{}
	if resourceID != "" {
		for range resourceDocumentIndex {
			metadataDocuments = append(metadataDocuments, modkit.MetadataDocument{Path: "other.json", Data: map[string]any{"resource_id": ""}})
		}
		metadataDocuments = append(metadataDocuments, modkit.MetadataDocument{Path: "mod_info/info.json", Data: map[string]any{"resource_id": resourceID}})
	}
	return ScanArchive{
		Root:        root,
		ArchivePath: path,
		SizeBytes:   sizeBytes,
		Modified:    modified,
		Manifest: modkit.Manifest{
			SchemaVersion:      modkit.SchemaVersion,
			AnalyzerVersion:    modkit.AnalyzerVersion,
			AnalyzedAt:         modified,
			ArchivePath:        path,
			Filename:           filename,
			SizeBytes:          sizeBytes,
			ModifiedAt:         modified,
			CentralFingerprint: fingerprint,
			FullSHA256:         sha256Value,
			ValidArchive:       true,
			Kind:               modkit.KindVehicle,
			Title:              title,
			Author:             author,
			Version:            version,
			MetadataDocuments:  metadataDocuments,
			Namespaces:         map[string][]string{},
			Members:            []modkit.ArchiveMember{},
		},
	}
}

func writeFamilyFixtureArchive(path string, sizeBytes int64) error {
	payload := []byte("mod-family-fixture")
	if sizeBytes > int64(len(payload)) {
		payload = append(payload, make([]byte, int(sizeBytes)-len(payload))...)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, payload, 0o644)
}
