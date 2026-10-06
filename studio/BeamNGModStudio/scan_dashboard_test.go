package main

import (
	"context"
	"errors"
	"testing"
)

func TestDashboardRetainsLastSuccessfulScanAcrossFailedAndCancelledAttempts(t *testing.T) {
	store, _ := openLibraryStorage(t)
	ctx := context.Background()
	setScanTimes := func(id, started, finished string) {
		t.Helper()
		if _, err := store.db.ExecContext(
			ctx,
			`UPDATE scans SET started_at=?,finished_at=? WHERE id=?`,
			started,
			finished,
			id,
		); err != nil {
			t.Fatal(err)
		}
	}

	firstID, err := store.BeginScan(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FinishScan(ctx, firstID, nil, 4, 3, 0, nil); err != nil {
		t.Fatal(err)
	}
	setScanTimes(firstID, "2026-01-01T00:00:00.9Z", "2026-01-01T00:00:01.9Z")
	first, err := store.Dashboard(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if first.LastScanStatus != "complete" || first.LastScanFound != 4 || first.LastScanAnalyzed != 3 || first.LastScanFailed != 0 {
		t.Fatalf("first scan dashboard = %#v", first)
	}
	if first.LastSuccessfulScanAt == "" || first.LastSuccessfulScanAt != first.LastScanAt {
		t.Fatalf("first successful timestamp = %q, latest attempt = %q", first.LastSuccessfulScanAt, first.LastScanAt)
	}
	lastSuccessful := first.LastSuccessfulScanAt

	failedID, err := store.BeginScan(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FinishScan(ctx, failedID, nil, 8, 5, 2, errors.New("fixture failure")); err != nil {
		t.Fatal(err)
	}
	setScanTimes(failedID, "2026-01-01T00:00:00.901Z", "2026-01-01T00:00:01.901Z")
	failed, err := store.Dashboard(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if failed.LastScanStatus != "failed" || failed.LastScanFound != 8 || failed.LastScanAnalyzed != 5 || failed.LastScanFailed != 2 {
		t.Fatalf("failed scan dashboard = %#v", failed)
	}
	if failed.LastSuccessfulScanAt != lastSuccessful {
		t.Fatalf("failed attempt changed last successful timestamp from %q to %q", lastSuccessful, failed.LastSuccessfulScanAt)
	}

	restartedAfterFailureID, err := store.BeginScan(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FinishScan(ctx, restartedAfterFailureID, nil, 5, 5, 0, nil); err != nil {
		t.Fatal(err)
	}
	setScanTimes(restartedAfterFailureID, "2026-01-01T00:00:00.902Z", "2026-01-01T00:00:01.902Z")
	restartedAfterFailure, err := store.Dashboard(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if restartedAfterFailure.LastScanStatus != "complete" || restartedAfterFailure.LastSuccessfulScanAt == lastSuccessful {
		t.Fatalf("restart after failure dashboard = %#v", restartedAfterFailure)
	}
	lastSuccessful = restartedAfterFailure.LastSuccessfulScanAt

	cancelledID, err := store.BeginScan(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FinishScan(ctx, cancelledID, nil, 6, 2, 0, context.Canceled); err != nil {
		t.Fatal(err)
	}
	setScanTimes(cancelledID, "2026-01-01T00:00:00.903Z", "2026-01-01T00:00:01.903Z")
	cancelled, err := store.Dashboard(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.LastScanStatus != "interrupted" {
		t.Fatalf("cancelled scan status = %q", cancelled.LastScanStatus)
	}
	if cancelled.LastSuccessfulScanAt != lastSuccessful {
		t.Fatalf("cancelled attempt changed last successful timestamp from %q to %q", lastSuccessful, cancelled.LastSuccessfulScanAt)
	}

	restartedAfterCancelID, err := store.BeginScan(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FinishScan(ctx, restartedAfterCancelID, nil, 2, 2, 0, nil); err != nil {
		t.Fatal(err)
	}
	setScanTimes(restartedAfterCancelID, "2026-01-01T00:00:00.904Z", "2026-01-01T00:00:01.904Z")
	restartedAfterCancel, err := store.Dashboard(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if restartedAfterCancel.LastScanStatus != "complete" || restartedAfterCancel.LastSuccessfulScanAt == lastSuccessful {
		t.Fatalf("restart after cancellation dashboard = %#v", restartedAfterCancel)
	}
}
