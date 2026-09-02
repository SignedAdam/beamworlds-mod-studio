package main

import (
	"archive/zip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	modkit "github.com/SignedAdam/beamworlds-modkit"
)

func TestInspectorMetadataUpdatesArchiveAndHistory(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	archivePath := writeInspectorArchiveFixture(t, service.config.BeamNGRoot)
	item := insertModAuditFixture(t, service, archivePath)

	updated, err := service.UpdateLibraryItemDetails(item.EntityID, LibraryItemDetailsUpdate{
		Description: "Changed description",
		Author:      "",
		Version:     "2.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Item.EntityID != item.EntityID || updated.Item.ArtifactID == item.ArtifactID {
		t.Fatalf("edited item was not reindexed onto a new artifact: %#v", updated.Item)
	}
	if updated.Item.Manifest.Description != "Changed description" || updated.Item.Manifest.Author != "" || updated.Item.Manifest.Version != "2.0" {
		t.Fatalf("updated details were not re-read from the archive: %#v", updated.Item.Manifest)
	}
	metadata := readInspectorJSONMember(t, archivePath, "mod_info/info.json")
	if metadata["description"] != "Changed description" || metadata["version"] != "2.0" {
		t.Fatalf("details metadata was not updated: %#v", metadata)
	}
	if _, exists := metadata["author"]; exists {
		t.Fatalf("cleared author remains in metadata: %#v", metadata)
	}
	if preserve, ok := metadata["preserve"].(map[string]any); !ok || preserve["value"] != true {
		t.Fatalf("unrelated details metadata was lost: %#v", metadata)
	}
	if !historyContains(updated.History, "metadata_updated") {
		t.Fatalf("metadata update was not recorded in history: %#v", updated.History)
	}

	cleared, err := service.UpdateLibraryItemDetails(item.EntityID, LibraryItemDetailsUpdate{Version: "2.0"})
	if err != nil {
		t.Fatal(err)
	}
	metadata = readInspectorJSONMember(t, archivePath, "mod_info/info.json")
	if _, lowerExists := metadata["description"]; lowerExists {
		t.Fatalf("lowercase description alias was not cleared: %#v", metadata)
	}
	if _, upperExists := metadata["Description"]; upperExists {
		t.Fatalf("duplicate description alias was not cleared: %#v", metadata)
	}
	if cleared.Item.Manifest.Description != "" {
		t.Fatalf("cleared description remained visible: %q", cleared.Item.Manifest.Description)
	}

	variant := cleared.Item.Manifest.Variants[0]
	beforeInvalid, err := modkit.FullSHA256(context.Background(), archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdateLibraryVariant(item.EntityID, LibraryVariantUpdate{ConfigPath: variant.ConfigPath, Power: "not-a-number"}); err == nil {
		t.Fatal("invalid numeric variant metadata was accepted")
	}
	afterInvalid, err := modkit.FullSHA256(context.Background(), archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if beforeInvalid != afterInvalid {
		t.Fatal("invalid variant update modified the archive")
	}

	variantDetail, err := service.UpdateLibraryVariant(item.EntityID, LibraryVariantUpdate{
		ConfigPath:    variant.ConfigPath,
		Configuration: "Track Edition",
		Description:   "Updated variant",
		ConfigType:    "Custom",
		BodyStyle:     "Coupe",
		Drivetrain:    "RWD",
		Transmission:  "Manual",
		FuelType:      "Gasoline",
		Propulsion:    "ICE",
		Power:         "250.5",
		Torque:        "",
		Weight:        "1300",
		Value:         "6000",
		TopSpeed:      "180",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(variantDetail.Item.Manifest.Variants) != 1 {
		t.Fatalf("variant count changed: %#v", variantDetail.Item.Manifest.Variants)
	}
	updatedVariant := variantDetail.Item.Manifest.Variants[0]
	if updatedVariant.Configuration != "Track Edition" || updatedVariant.ConfigType != "Custom" || updatedVariant.Power != 250.5 || updatedVariant.Torque != 0 || updatedVariant.TopSpeed != 180 {
		t.Fatalf("variant update was not re-read from the archive: %#v", updatedVariant)
	}
	variantMetadata := readInspectorJSONMember(t, archivePath, "vehicles/test/info_custom.json")
	if variantMetadata["preserve"] != "yes" || variantMetadata["Configuration"] != "Track Edition" {
		t.Fatalf("variant metadata was not preserved and updated: %#v", variantMetadata)
	}
	if _, exists := variantMetadata["Torque"]; exists {
		t.Fatalf("cleared torque remains in variant metadata: %#v", variantMetadata)
	}
	if !historyContains(variantDetail.History, "variant_updated") {
		t.Fatalf("variant update was not recorded in history: %#v", variantDetail.History)
	}
}

func TestInspectorArchiveMemberPreviews(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	archivePath := writeInspectorArchiveFixture(t, service.config.BeamNGRoot)
	item := insertModAuditFixture(t, service, archivePath)

	text, err := service.PreviewLibraryArchiveMember(item.EntityID, "notes.txt")
	if err != nil {
		t.Fatal(err)
	}
	if text.Kind != "text" || text.MIME != "text/plain" || text.Text != "Inspector notes\n" || text.Truncated {
		t.Fatalf("unexpected text preview: %#v", text)
	}
	image, err := service.PreviewLibraryArchiveMember(item.EntityID, "vehicles/test/custom.png")
	if err != nil {
		t.Fatal(err)
	}
	if image.Kind != "image" || !strings.HasPrefix(image.DataURL, "data:image/png;base64,") {
		t.Fatalf("unexpected image preview: %#v", image)
	}
	binary, err := service.PreviewLibraryArchiveMember(item.EntityID, "payload.bin")
	if err != nil {
		t.Fatal(err)
	}
	if binary.Kind != "binary" || binary.MIME != "application/octet-stream" || binary.DataURL != "" || binary.Text != "" {
		t.Fatalf("unexpected binary preview: %#v", binary)
	}
	if _, err := service.PreviewLibraryArchiveMember(item.EntityID, "../notes.txt"); err == nil {
		t.Fatal("unsafe preview path was accepted")
	}
	if _, err := service.PreviewLibraryArchiveMember(item.EntityID, "missing.txt"); err == nil {
		t.Fatal("missing preview member was accepted")
	}
}

func writeInspectorArchiveFixture(t *testing.T, root string) string {
	t.Helper()
	archivePath := filepath.Join(root, "inspector-fixture.zip")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	entries := []struct {
		path string
		data []byte
	}{
		{path: "mod_info/info.json", data: []byte(`{"title":"Inspector Fixture","description":"Original","Description":"Duplicate description","author":"Original Author","version":"1.0","preserve":{"value":true}}`)},
		{path: "vehicles/test/custom.pc", data: []byte(`{"format":2}`)},
		{path: "vehicles/test/info_custom.json", data: []byte(`{"Configuration":"Original Trim","configuration":"Duplicate Trim","Description":"Original variant","Config Type":"Factory","Power":100,"Torque":200,"Weight":1200,"Value":5000,"Top Speed":150,"preserve":"yes"}`)},
		{path: "vehicles/test/custom.png", data: rasterImageFixture()},
		{path: "notes.txt", data: []byte("Inspector notes\n")},
		{path: "payload.bin", data: []byte{0, 1, 2, 3}},
	}
	for _, entry := range entries {
		destination, err := writer.Create(entry.path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := destination.Write(entry.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return archivePath
}

func readInspectorJSONMember(t *testing.T, archivePath, memberPath string) map[string]any {
	t.Helper()
	data, err := modkit.ReadArchiveMember(archivePath, memberPath, modkit.MaxArchiveJSONBytes)
	if err != nil {
		t.Fatal(err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(data, &metadata); err != nil {
		t.Fatal(err)
	}
	return metadata
}

func historyContains(history []EventRecord, eventType string) bool {
	for _, event := range history {
		if event.Type == eventType {
			return true
		}
	}
	return false
}
