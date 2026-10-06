package main

import (
	"errors"
	"strings"
	"testing"
)

func TestValidatePlaySelectionRequiresCurrentReviewFingerprint(t *testing.T) {
	selection := PlaySelection{
		Fingerprint: "fingerprint-current",
		Mods:        []CollectionMod{{EntityID: "entity-1", DisplayName: "One", Available: true}},
	}
	if err := validatePlaySelectionRequest(PlayRequest{Fingerprint: "fingerprint-old"}, selection); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("stale fingerprint error = %v", err)
	}
	if err := validatePlaySelectionRequest(PlayRequest{Fingerprint: "fingerprint-current"}, selection); err != nil {
		t.Fatalf("current fingerprint rejected: %v", err)
	}
	if err := validatePlaySelectionRequest(PlayRequest{Fingerprint: "empty-selection"}, PlaySelection{Fingerprint: "empty-selection"}); err != nil {
		t.Fatalf("empty exact selection rejected: %v", err)
	}
}

func TestValidatePlaySelectionRejectsMissingArchivesBeforeActivation(t *testing.T) {
	selection := PlaySelection{
		Fingerprint:  "fingerprint-current",
		MissingCount: 1,
		Mods:         []CollectionMod{{EntityID: "entity-1", DisplayName: "Unavailable", Available: false}},
	}
	if err := validatePlaySelectionRequest(PlayRequest{Fingerprint: selection.Fingerprint}, selection); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("missing archive was accepted: %v", err)
	}
}

func TestClassifyProcessLaunchFailureDistinguishesStartedProcess(t *testing.T) {
	service := &AppService{gameRunning: func() (bool, error) { return false, nil }}
	started, uncertain, message := service.classifyProcessLaunchFailure(ProcessLaunch{PID: 42}, errors.New("release failed"))
	if !started || !uncertain || !strings.Contains(message, "started") {
		t.Fatalf("started process outcome = started %t uncertain %t message %q", started, uncertain, message)
	}

	started, uncertain, message = service.classifyProcessLaunchFailure(ProcessLaunch{}, errors.New("executable missing"))
	if started || uncertain || !strings.Contains(message, "did not start") {
		t.Fatalf("failed process outcome = started %t uncertain %t message %q", started, uncertain, message)
	}
}
