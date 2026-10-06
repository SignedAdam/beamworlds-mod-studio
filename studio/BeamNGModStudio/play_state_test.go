package main

import (
	"testing"
)

func TestPlayStateNormalizesDeletedReferencesWithoutOverwritingProfile(t *testing.T) {
	service := newTestAppService(t)
	created, err := service.CreateCollection("Draft collection", "", "")
	if err != nil {
		t.Fatal(err)
	}
	profile, err := service.CreatePlayProfile([]string{created.Collection.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	state, err := service.SavePlayState(PlayState{
		ProfileID:            profile.ID,
		CollectionIDs:        []string{created.Collection.ID, "deleted-collection", created.Collection.ID},
		DefaultCollectionIDs: []string{created.Collection.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if state.ProfileID != profile.ID || len(state.CollectionIDs) != 1 || state.CollectionIDs[0] != created.Collection.ID {
		t.Fatalf("normalized Play state = %#v", state)
	}

	if _, err := service.DeleteCollections([]string{created.Collection.ID}); err != nil {
		t.Fatal(err)
	}
	restored, err := service.GetPlayState()
	if err != nil {
		t.Fatal(err)
	}
	if restored.ProfileID != profile.ID || len(restored.CollectionIDs) != 0 || len(restored.DefaultCollectionIDs) != 0 {
		t.Fatalf("deleted collection changed profile identity or draft normalization: %#v", restored)
	}
	organization, err := service.Organization()
	if err != nil {
		t.Fatal(err)
	}
	if len(organization.Profiles) != 1 || organization.Profiles[0].ID != profile.ID {
		t.Fatalf("saved profile was overwritten by Play state normalization: %#v", organization.Profiles)
	}
}
