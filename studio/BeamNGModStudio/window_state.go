package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"sync"
	"time"
)

const (
	windowStateSettingKey = "app.window.v1"
	// A resize is saved once the window has been still for this long, so a
	// drag is one write instead of one per frame.
	windowStateSaveDelay = 5 * time.Second
	defaultWindowWidth   = 1500
	defaultWindowHeight  = 940
	minimumWindowWidth   = 1120
	minimumWindowHeight  = 720
)

// windowState is the main window size in device-independent pixels. While the
// window is maximised it keeps the last normal size, which un-maximising
// returns to.
type windowState struct {
	Width     int  `json:"width"`
	Height    int  `json:"height"`
	Maximised bool `json:"maximised"`
}

// loadWindowState returns the saved size, or the default for a first start or
// an unreadable value.
func (s *Store) loadWindowState(ctx context.Context) (windowState, error) {
	state := windowState{Width: defaultWindowWidth, Height: defaultWindowHeight}
	encoded, err := s.readSetting(ctx, windowStateSettingKey)
	if errors.Is(err, sql.ErrNoRows) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	var saved windowState
	if json.Unmarshal([]byte(encoded), &saved) != nil || saved.Width <= 0 || saved.Height <= 0 {
		return state, nil
	}
	saved.Width = max(saved.Width, minimumWindowWidth)
	saved.Height = max(saved.Height, minimumWindowHeight)
	return saved, nil
}

func (s *Store) saveWindowState(ctx context.Context, state windowState) error {
	encoded, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return s.writeSetting(ctx, windowStateSettingKey, string(encoded))
}

// nextWindowState folds the window's current size into the saved state. A
// minimised window has nothing worth saving; a maximised one keeps the
// previous normal size.
func nextWindowState(previous windowState, width, height int, maximised, minimised bool) (windowState, bool) {
	if minimised || width <= 0 || height <= 0 {
		return previous, false
	}
	if maximised {
		previous.Maximised = true
		return previous, true
	}
	return windowState{Width: max(width, minimumWindowWidth), Height: max(height, minimumWindowHeight)}, true
}

type stoppableTimer interface{ Stop() bool }

// windowStateSaver saves the window size after resizing has stopped for the
// configured delay. Every resize restarts the countdown.
type windowStateSaver struct {
	mu         sync.Mutex
	delay      time.Duration
	timer      stoppableTimer
	generation uint64
	pending    bool
	saved      windowState
	sample     func() (width, height int, maximised, minimised bool)
	save       func(windowState) error
	schedule   func(time.Duration, func()) stoppableTimer
}

func newWindowStateSaver(saved windowState, delay time.Duration, sample func() (int, int, bool, bool), save func(windowState) error) *windowStateSaver {
	return &windowStateSaver{
		delay:  delay,
		saved:  saved,
		sample: sample,
		save:   save,
		schedule: func(delay time.Duration, run func()) stoppableTimer {
			return time.AfterFunc(delay, run)
		},
	}
}

// resized records that the window changed and restarts the countdown.
func (s *windowStateSaver) resized() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending = true
	s.generation++
	generation := s.generation
	if s.timer != nil {
		s.timer.Stop()
	}
	s.timer = s.schedule(s.delay, func() { s.commit(generation) })
}

// flush saves a pending change immediately; the window is about to close.
func (s *windowStateSaver) flush() {
	s.mu.Lock()
	if !s.pending {
		s.mu.Unlock()
		return
	}
	if s.timer != nil {
		s.timer.Stop()
	}
	generation := s.generation
	s.mu.Unlock()
	s.commit(generation)
}

func (s *windowStateSaver) commit(generation uint64) {
	s.mu.Lock()
	if !s.pending || generation != s.generation {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	// Reading the window waits for the UI thread, so it happens unlocked.
	width, height, maximised, minimised := s.sample()
	s.mu.Lock()
	defer s.mu.Unlock()
	if generation != s.generation {
		return // resized again while sampling; the newer countdown saves it
	}
	s.pending = false
	next, ok := nextWindowState(s.saved, width, height, maximised, minimised)
	if !ok || next == s.saved {
		return
	}
	if err := s.save(next); err != nil {
		log.Printf("save window size: %v", err)
		return
	}
	s.saved = next
}
