package main

import (
	"context"
	"testing"
	"time"
)

type manualTimer struct {
	run     func()
	stopped bool
}

func (timer *manualTimer) Stop() bool {
	wasActive := !timer.stopped
	timer.stopped = true
	return wasActive
}

type windowHarness struct {
	saver   *windowStateSaver
	timers  []*manualTimer
	saves   []windowState
	width   int
	height  int
	maximum bool
	minimum bool
}

func newWindowHarness(saved windowState) *windowHarness {
	harness := &windowHarness{width: saved.Width, height: saved.Height}
	harness.saver = newWindowStateSaver(saved, windowStateSaveDelay,
		func() (int, int, bool, bool) { return harness.width, harness.height, harness.maximum, harness.minimum },
		func(state windowState) error { harness.saves = append(harness.saves, state); return nil },
	)
	harness.saver.schedule = func(delay time.Duration, run func()) stoppableTimer {
		if delay != windowStateSaveDelay {
			panic("unexpected save delay")
		}
		timer := &manualTimer{run: run}
		harness.timers = append(harness.timers, timer)
		return timer
	}
	return harness
}

// elapse runs every countdown that is still live, as if the delay passed.
func (harness *windowHarness) elapse() {
	for _, timer := range harness.timers {
		if !timer.stopped {
			timer.stopped = true
			timer.run()
		}
	}
}

func TestWindowSizeIsSavedOnceAfterResizingStops(t *testing.T) {
	harness := newWindowHarness(windowState{Width: 1500, Height: 940})
	for _, width := range []int{1510, 1600, 1700, 1800} {
		harness.width, harness.height = width, 1000
		harness.saver.resized()
	}
	if len(harness.saves) != 0 {
		t.Fatalf("saved while still resizing: %#v", harness.saves)
	}
	// A countdown that fired just before a later resize must not save early.
	harness.timers[0].run()
	if len(harness.saves) != 0 {
		t.Fatalf("an outdated countdown saved: %#v", harness.saves)
	}
	harness.elapse()
	if len(harness.saves) != 1 || harness.saves[0] != (windowState{Width: 1800, Height: 1000}) {
		t.Fatalf("saves after the window stopped = %#v", harness.saves)
	}
	harness.saver.resized()
	harness.elapse()
	if len(harness.saves) != 1 {
		t.Fatalf("an unchanged size was written again: %#v", harness.saves)
	}
}

func TestClosingDuringCountdownKeepsTheLatestSize(t *testing.T) {
	harness := newWindowHarness(windowState{Width: 1500, Height: 940})
	harness.width, harness.height = 1300, 800
	harness.saver.resized()
	harness.saver.flush()
	if len(harness.saves) != 1 || harness.saves[0] != (windowState{Width: 1300, Height: 800}) {
		t.Fatalf("close lost the pending size: %#v", harness.saves)
	}
	harness.elapse()
	harness.saver.flush()
	if len(harness.saves) != 1 {
		t.Fatalf("flushed size was saved twice: %#v", harness.saves)
	}
}

func TestMaximisedWindowKeepsItsNormalSize(t *testing.T) {
	harness := newWindowHarness(windowState{Width: 1400, Height: 900})
	harness.width, harness.height, harness.maximum = 2560, 1392, true
	harness.saver.resized()
	harness.elapse()
	if len(harness.saves) != 1 || harness.saves[0] != (windowState{Width: 1400, Height: 900, Maximised: true}) {
		t.Fatalf("maximising replaced the normal size: %#v", harness.saves)
	}
	harness.width, harness.height, harness.maximum, harness.minimum = 0, 0, false, true
	harness.saver.resized()
	harness.elapse()
	if len(harness.saves) != 1 {
		t.Fatalf("a minimised window was saved: %#v", harness.saves)
	}
	harness.width, harness.height, harness.minimum = 1400, 900, false
	harness.saver.resized()
	harness.elapse()
	if last := harness.saves[len(harness.saves)-1]; last != (windowState{Width: 1400, Height: 900}) {
		t.Fatalf("restoring from maximised kept the maximised flag: %#v", harness.saves)
	}
}

func TestSavedWindowSizeRespectsTheMinimum(t *testing.T) {
	service := newTestAppService(t)
	ctx := context.Background()
	if state, err := service.store.loadWindowState(ctx); err != nil || state != (windowState{Width: defaultWindowWidth, Height: defaultWindowHeight}) {
		t.Fatalf("first start = %#v, %v", state, err)
	}
	if err := service.store.saveWindowState(ctx, windowState{Width: 300, Height: 200}); err != nil {
		t.Fatal(err)
	}
	if state, err := service.store.loadWindowState(ctx); err != nil || state != (windowState{Width: minimumWindowWidth, Height: minimumWindowHeight}) {
		t.Fatalf("undersized saved window = %#v, %v", state, err)
	}
	if err := service.store.writeSetting(ctx, windowStateSettingKey, "not json"); err != nil {
		t.Fatal(err)
	}
	if state, err := service.store.loadWindowState(ctx); err != nil || state != (windowState{Width: defaultWindowWidth, Height: defaultWindowHeight}) {
		t.Fatalf("unreadable saved window = %#v, %v", state, err)
	}
}
