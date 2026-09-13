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
func TestModFamiliesContentGroupingAuthorlessEqualNamespaces(t *testing.T) {
	service := newTestAppService(t)
	root := filepath.Join(service.config.DataDir, "library")
	archives := []ScanArchive{
		modFamilyScanArchive(t, root, "content-one.zip", "  Content   Family ", "", "", "", "content-sha-one", "content-fingerprint-one", 100, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), 0),
		modFamilyScanArchive(t, root, "content-two.zip", "content family", "", "", "", "content-sha-two", "content-fingerprint-two", 120, time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC), 0),
	}
	archives[0].Manifest.Namespaces = map[string][]string{
		"vehicles": {"a6", "common"},
		"levels":   {"a6"},
	}
	archives[1].Manifest.Namespaces = map[string][]string{
		"levels":   {"a6"},
		"vehicles": {"common", "a6"},
	}
	applyLibraryArchives(t, service.store, root, archives)

	families, err := service.ModFamilies()
	if err != nil {
		t.Fatal(err)
	}
	if len(families) != 1 || families[0].Confidence != "content" || !strings.HasPrefix(families[0].ID, "content:") {
		t.Fatalf("families = %#v, want one content family", families)
	}
	if got := families[0].Members[0].Namespaces; len(got) != 2 || got[0] != "a6" || got[1] != "common" {
		t.Fatalf("content namespaces = %#v, want sorted flattened set", got)
	}
}

func TestModFamiliesContentGroupingDifferentNamespacesDoesNotMatch(t *testing.T) {
	service := newTestAppService(t)
	root := filepath.Join(service.config.DataDir, "library")
	archives := []ScanArchive{
		modFamilyScanArchive(t, root, "content-different-one.zip", "Same Content Title", "", "", "", "content-different-sha-one", "content-different-one", 100, time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC), 0),
		modFamilyScanArchive(t, root, "content-different-two.zip", "Same Content Title", "", "", "", "content-different-sha-two", "content-different-two", 100, time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC), 0),
	}
	archives[0].Manifest.Namespaces = map[string][]string{"vehicles": {"a6"}}
	archives[1].Manifest.Namespaces = map[string][]string{"vehicles": {"a7"}}
	applyLibraryArchives(t, service.store, root, archives)

	families, err := service.ModFamilies()
	if err != nil {
		t.Fatal(err)
	}
	if len(families) != 0 {
		t.Fatalf("families = %#v, want no family for different namespace sets", families)
	}
}

func TestModFamiliesContentGroupingEmptyNamespacesDoesNotMatch(t *testing.T) {
	service := newTestAppService(t)
	root := filepath.Join(service.config.DataDir, "library")
	archives := []ScanArchive{
		modFamilyScanArchive(t, root, "content-empty-one.zip", "Same Empty Title", "Author One", "", "", "content-empty-sha-one", "content-empty-one", 100, time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC), 0),
		modFamilyScanArchive(t, root, "content-empty-two.zip", "Same Empty Title", "Author Two", "", "", "content-empty-sha-two", "content-empty-two", 100, time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC), 0),
	}
	archives[0].Manifest.Namespaces = map[string][]string{}
	archives[1].Manifest.Namespaces = map[string][]string{"vehicles": {}}
	applyLibraryArchives(t, service.store, root, archives)

	families, err := service.ModFamilies()
	if err != nil {
		t.Fatal(err)
	}
	if len(families) != 0 {
		t.Fatalf("families = %#v, want no family when all namespaces are empty", families)
	}
}

func TestModFamiliesInferenceTierExclusivity(t *testing.T) {
	service := newTestAppService(t)
	root := filepath.Join(service.config.DataDir, "library")
	archives := []ScanArchive{
		modFamilyScanArchive(t, root, "tier-repo-one.zip", "Tier Repo", "Same Author", "", "tier-repo", "tier-repo-sha-one", "tier-repo-one", 100, time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC), 0),
		modFamilyScanArchive(t, root, "tier-repo-two.zip", "Tier Repo", "Same Author", "", "tier-repo", "tier-repo-sha-two", "tier-repo-two", 100, time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC), 0),
		modFamilyScanArchive(t, root, "tier-content-one.zip", "Tier Content", "Same Author", "", "", "tier-content-sha-one", "tier-content-one", 100, time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC), 0),
		modFamilyScanArchive(t, root, "tier-content-two.zip", "Tier Content", "Same Author", "", "", "tier-content-sha-two", "tier-content-two", 100, time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC), 0),
		modFamilyScanArchive(t, root, "tier-metadata-one.zip", "Tier Metadata", "Same Author", "", "", "tier-metadata-sha-one", "tier-metadata-one", 100, time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC), 0),
		modFamilyScanArchive(t, root, "tier-metadata-two.zip", "Tier Metadata", "Same Author", "", "", "tier-metadata-sha-two", "tier-metadata-two", 100, time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC), 0),
	}
	for index := range archives[:4] {
		archives[index].Manifest.Namespaces = map[string][]string{"vehicles": {"tier-car"}}
	}
	archives[4].Manifest.Namespaces = map[string][]string{"vehicles": {"other-car"}}
	archives[5].Manifest.Namespaces = map[string][]string{"vehicles": {"different-car"}}
	applyLibraryArchives(t, service.store, root, archives)

	families, err := service.ModFamilies()
	if err != nil {
		t.Fatal(err)
	}
	if len(families) != 3 {
		t.Fatalf("families = %#v, want one family at each inferred tier", families)
	}
	byConfidence := make(map[string]ModFamily, len(families))
	for _, family := range families {
		byConfidence[family.Confidence] = family
	}
	if family := byConfidence["repo"]; family.ID != "repo:tier-repo" || len(family.Members) != 2 {
		t.Fatalf("repo family = %#v, want repo tier to win over lower tiers", family)
	}
	if family := byConfidence["content"]; len(family.Members) != 2 || family.Title != "Tier Content" {
		t.Fatalf("content family = %#v, want content tier to win over metadata", family)
	}
	if family := byConfidence["metadata"]; len(family.Members) != 2 || family.Title != "Tier Metadata" {
		t.Fatalf("metadata family = %#v", family)
	}
}

func TestModFamiliesComparisonFieldsPopulateManifestAndHealthData(t *testing.T) {
	service := newTestAppService(t)
	root := filepath.Join(service.config.DataDir, "library")
	archives := []ScanArchive{
		modFamilyScanArchive(t, root, "comparison-one.zip", "Comparison Family", "", "", "", "comparison-sha-one", "comparison-one", 100, time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC), 0),
		modFamilyScanArchive(t, root, "comparison-two.zip", "Comparison Family", "", "", "", "comparison-sha-two", "comparison-two", 100, time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC), 0),
	}
	archives[0].Manifest.Namespaces = map[string][]string{"vehicles": {"comparison-car"}, "levels": {"comparison-level"}}
	archives[0].Manifest.Members = []modkit.ArchiveMember{{Path: "one"}, {Path: "two"}, {Path: "three"}}
	archives[0].Manifest.Variants = []modkit.Variant{{Namespace: "comparison-car"}, {Namespace: "comparison-car"}}
	archives[0].Manifest.Issues = []modkit.Issue{{Severity: modkit.SeverityInfo}, {Severity: modkit.SeverityWarning}}
	archives[1].Manifest.Namespaces = map[string][]string{"levels": {"comparison-level"}, "vehicles": {"comparison-car"}}
	archives[1].Manifest.Members = []modkit.ArchiveMember{{Path: "only"}}
	archives[1].Manifest.Variants = []modkit.Variant{{Namespace: "comparison-car"}}
	archives[1].Manifest.Issues = []modkit.Issue{{Severity: modkit.SeverityInfo}, {Severity: modkit.SeverityError}}
	applyLibraryArchives(t, service.store, root, archives)

	families, err := service.ModFamilies()
	if err != nil {
		t.Fatal(err)
	}
	if len(families) != 1 || families[0].Confidence != "content" {
		t.Fatalf("families = %#v, want one content family", families)
	}
	byPath := make(map[string]ModFamilyMember, len(families[0].Members))
	for _, member := range families[0].Members {
		byPath[filepath.Base(member.ArchivePath)] = member
	}
	first := byPath["comparison-one.zip"]
	if first.EntryCount != 3 || first.VariantCount != 2 || first.IssueCount != 2 || first.IssueSeverity != "warning" ||
		first.HealthStatus != "unscanned" || len(first.Namespaces) != 2 || first.Namespaces[0] != "comparison-car" || first.Namespaces[1] != "comparison-level" {
		t.Fatalf("first comparison member = %#v", first)
	}
	second := byPath["comparison-two.zip"]
	if second.EntryCount != 1 || second.VariantCount != 1 || second.IssueCount != 2 || second.IssueSeverity != "error" || second.HealthStatus != "broken" {
		t.Fatalf("second comparison member = %#v", second)
	}
}

func TestModFamiliesInstalledInGameUsesActiveBeamNGDatabaseNames(t *testing.T) {
	service := newTestAppService(t)
	root := filepath.Join(service.config.DataDir, "library")
	archives := []ScanArchive{
		modFamilyScanArchive(t, root, "installed-family.zip", "Installed Family", "", "", "", "installed-sha", "installed-family", 100, time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC), 0),
		modFamilyScanArchive(t, root, "not-installed-family.zip", "Installed Family", "", "", "", "not-installed-sha", "not-installed-family", 100, time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC), 0),
	}
	for index := range archives {
		archives[index].Manifest.Namespaces = map[string][]string{"vehicles": {"installed-family-car"}}
	}
	applyLibraryArchives(t, service.store, root, archives)
	database := []byte(`{"mods":{"active-entry":{"active":true,"filename":"installed-family.zip","fullpath":"mods"},"inactive-entry":{"active":false,"filename":"not-installed-family.zip","fullpath":"mods"}}}`)
	if err := os.WriteFile(filepath.Join(service.config.ActiveModsDir, "db.json"), database, 0o644); err != nil {
		t.Fatal(err)
	}

	families, err := service.ModFamilies()
	if err != nil {
		t.Fatal(err)
	}
	if len(families) != 1 || families[0].Confidence != "content" {
		t.Fatalf("families = %#v, want one content family", families)
	}
	installed := 0
	for _, member := range families[0].Members {
		if member.InstalledInGame {
			installed++
			if filepath.Base(member.ArchivePath) != "installed-family.zip" {
				t.Fatalf("unexpected installed member = %#v", member)
			}
		}
	}
	if installed != 1 {
		t.Fatalf("installed member count = %d, want only active database name", installed)
	}
}

func TestModFamiliesExcludesArchivedModsIncludingFileFamilies(t *testing.T) {
	service := newTestAppService(t)
	root := filepath.Join(service.config.DataDir, "library")
	archives := []ScanArchive{
		modFamilyScanArchive(t, root, filepath.Join("archived-one", "same-name.zip"), "Archived Copies", "Archive Author", "", "", "archived-sha", "archived-fingerprint", 100, time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC), 0),
		modFamilyScanArchive(t, root, filepath.Join("archived-two", "same-name.zip"), "Archived Copies", "Archive Author", "", "", "archived-sha", "archived-fingerprint", 120, time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC), 0),
	}
	items := applyLibraryArchives(t, service.store, root, archives)
	if len(items) != 1 {
		t.Fatalf("items = %#v, want one entity with two links", items)
	}
	if _, err := service.store.db.Exec(`UPDATE entities SET archived_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), items[0].EntityID); err != nil {
		t.Fatal(err)
	}

	families, err := service.ModFamilies()
	if err != nil {
		t.Fatal(err)
	}
	if len(families) != 0 {
		t.Fatalf("families = %#v, want archived files family excluded", families)
	}
}
