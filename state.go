// state.go — the loop's durable state: state.json and events.jsonl.
//
// state.json holds the current item and stage; events.jsonl is append-only,
// one JSON object per line, and is the audit trail. State writes are atomic
// (temp file + rename) so a killed run never leaves a half-written state.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// State is the loop's current position for one item.
type State struct {
	Issue               string    `json:"issue"`
	Stage               string    `json:"stage"`
	Status              string    `json:"status"`
	SendBacks           int       `json:"send_backs"`
	ConsecutiveFailures int       `json:"consecutive_failures"`
	PRNumber            int       `json:"pr_number,omitempty"`
	Branch              string    `json:"branch,omitempty"`
	StartedAt           time.Time `json:"started_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// Event is one append-only audit record.
type Event struct {
	TS     string `json:"ts"`
	Stage  string `json:"stage"`
	Event  string `json:"event"`
	Detail string `json:"detail,omitempty"`
}

// Store reads and writes state.json and events.jsonl in a directory.
type Store struct {
	Dir string
}

// NewStore returns a store rooted at dir.
func NewStore(dir string) *Store {
	return &Store{Dir: dir}
}

func (s *Store) statePath() string { return filepath.Join(s.Dir, "state.json") }
func (s *Store) eventsPath() string {
	return filepath.Join(s.Dir, "events.jsonl")
}

// SaveState writes state.json atomically.
func (s *Store) SaveState(st *State) error {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return fmt.Errorf("creating state dir: %w", err)
	}
	st.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding state: %w", err)
	}
	tmp := s.statePath() + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("writing state: %w", err)
	}
	if err := os.Rename(tmp, s.statePath()); err != nil {
		return fmt.Errorf("committing state: %w", err)
	}
	return nil
}

// LoadState reads state.json. It returns os.ErrNotExist when no state
// exists yet.
func (s *Store) LoadState() (*State, error) {
	data, err := os.ReadFile(s.statePath())
	if err != nil {
		return nil, err
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("decoding state: %w", err)
	}
	return &st, nil
}

// AppendEvent appends one event to events.jsonl.
func (s *Store) AppendEvent(e Event) error {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return fmt.Errorf("creating state dir: %w", err)
	}
	if e.TS == "" {
		e.TS = time.Now().UTC().Format(time.RFC3339)
	}
	data, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("encoding event: %w", err)
	}
	f, err := os.OpenFile(s.eventsPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("opening events log: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("writing event: %w", err)
	}
	return nil
}

// ReadEvents returns every event in events.jsonl, oldest first. It
// returns an empty slice when the log does not exist yet.
func (s *Store) ReadEvents() ([]Event, error) {
	data, err := os.ReadFile(s.eventsPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []Event
	for _, line := range splitLinesKeepEmpty(data) {
		if len(line) == 0 {
			continue
		}
		var e Event
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, fmt.Errorf("decoding event: %w", err)
		}
		out = append(out, e)
	}
	return out, nil
}

func splitLinesKeepEmpty(data []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			out = append(out, data[start:i])
			start = i + 1
		}
	}
	if start < len(data) {
		out = append(out, data[start:])
	}
	return out
}
