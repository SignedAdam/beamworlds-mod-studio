package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
)

func (service *AppService) GetPlayState() (PlayState, error) {
	return service.store.GetPlayState(context.Background())
}

func (service *AppService) SavePlayState(state PlayState) (PlayState, error) {
	return service.store.SavePlayState(context.Background(), state)
}

func (s *Store) GetPlayState(ctx context.Context) (PlayState, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PlayState{}, err
	}
	defer tx.Rollback()
	state, exists, err := readPlayStateTx(ctx, tx)
	if err != nil {
		return PlayState{}, err
	}
	if !exists {
		return emptyPlayState(), nil
	}
	normalized, err := normalizePlayStateTx(ctx, tx, state)
	if err != nil {
		return PlayState{}, err
	}
	if !reflect.DeepEqual(state, normalized) {
		if err := writePlayStateTx(ctx, tx, normalized); err != nil {
			return PlayState{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return PlayState{}, err
	}
	return normalized, nil
}

func (s *Store) SavePlayState(ctx context.Context, state PlayState) (PlayState, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PlayState{}, err
	}
	defer tx.Rollback()
	normalized, err := normalizePlayStateTx(ctx, tx, state)
	if err != nil {
		return PlayState{}, err
	}
	if err := writePlayStateTx(ctx, tx, normalized); err != nil {
		return PlayState{}, err
	}
	if err := tx.Commit(); err != nil {
		return PlayState{}, err
	}
	return normalized, nil
}

func emptyPlayState() PlayState {
	return PlayState{CollectionIDs: []string{}, ExcludedCollectionIDs: []string{}, DefaultCollectionIDs: []string{}, DefaultExcludedCollectionIDs: []string{}, Notices: []string{}}
}

func readPlayStateTx(ctx context.Context, tx *sql.Tx) (PlayState, bool, error) {
	var payload string
	err := tx.QueryRowContext(ctx, `SELECT state_json FROM play_state WHERE id=1`).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return emptyPlayState(), false, nil
	}
	if err != nil {
		return PlayState{}, false, err
	}
	if strings.TrimSpace(payload) == "" {
		return PlayState{}, true, errors.New("saved Play state is empty")
	}
	var state PlayState
	if err := json.Unmarshal([]byte(payload), &state); err != nil {
		return PlayState{}, true, fmt.Errorf("parse saved Play state: %w", err)
	}
	return state, true, nil
}

func writePlayStateTx(ctx context.Context, tx *sql.Tx, state PlayState) error {
	payload, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode Play state: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO play_state(id,state_json) VALUES(1,?)
		ON CONFLICT(id) DO UPDATE SET state_json=excluded.state_json`, string(payload))
	return err
}

func normalizePlayStateTx(ctx context.Context, tx *sql.Tx, input PlayState) (PlayState, error) {
	state := input
	var err error
	if state.CollectionIDs, err = normalizePlayStateReferences(ctx, tx, state.CollectionIDs, &state.Notices); err != nil {
		return PlayState{}, err
	}
	if state.ExcludedCollectionIDs, err = normalizePlayStateReferences(ctx, tx, state.ExcludedCollectionIDs, &state.Notices); err != nil {
		return PlayState{}, err
	}
	if state.DefaultCollectionIDs, err = normalizePlayStateReferences(ctx, tx, state.DefaultCollectionIDs, &state.Notices); err != nil {
		return PlayState{}, err
	}
	if state.DefaultExcludedCollectionIDs, err = normalizePlayStateReferences(ctx, tx, state.DefaultExcludedCollectionIDs, &state.Notices); err != nil {
		return PlayState{}, err
	}
	state.ProfileID = strings.TrimSpace(state.ProfileID)
	state.Notices = normalizePlayStateNotices(state.Notices)
	if state.ProfileID != "" {
		var exists int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM play_profiles WHERE id=? LIMIT 1`, state.ProfileID).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			state.Notices = appendPlayStateNotice(state.Notices, "Your saved profile was deleted. Your mod selection is now under Default.")
			state.ProfileID = ""
		} else if err != nil {
			return PlayState{}, err
		}
	}
	if state.CollectionIDs == nil {
		state.CollectionIDs = []string{}
	}
	if state.ExcludedCollectionIDs == nil {
		state.ExcludedCollectionIDs = []string{}
	}
	if state.DefaultCollectionIDs == nil {
		state.DefaultCollectionIDs = []string{}
	}
	if state.DefaultExcludedCollectionIDs == nil {
		state.DefaultExcludedCollectionIDs = []string{}
	}
	if state.Notices == nil {
		state.Notices = []string{}
	}
	return state, nil
}

func normalizePlayStateReferences(ctx context.Context, tx *sql.Tx, values []string, notices *[]string) ([]string, error) {
	normalized := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		// The all-mods sentinel is valid without a real collection row.
		if value == AllModsCollectionID {
			normalized = append(normalized, value)
			continue
		}
		var exists int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM collections WHERE id=? LIMIT 1`, value).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			*notices = appendPlayStateNotice(*notices, "A collection in your saved Play selection was deleted. Review your selection before playing.")
			continue
		}
		if err != nil {
			return nil, err
		}
		normalized = append(normalized, value)
	}
	return normalized, nil
}

func normalizePlayStateNotices(notices []string) []string {
	normalized := make([]string, 0, len(notices))
	seen := make(map[string]struct{}, len(notices))
	for _, notice := range notices {
		notice = strings.TrimSpace(notice)
		if notice == "" {
			continue
		}
		if _, exists := seen[notice]; exists {
			continue
		}
		seen[notice] = struct{}{}
		normalized = append(normalized, notice)
	}
	return normalized
}

func appendPlayStateNotice(notices []string, notice string) []string {
	notice = strings.TrimSpace(notice)
	if notice == "" {
		return notices
	}
	for _, existing := range notices {
		if existing == notice {
			return notices
		}
	}
	return append(notices, notice)
}
