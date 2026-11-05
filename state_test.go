package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "state"))

	if _, err := store.LoadState(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("LoadState on a fresh store should return ErrNotExist, got %v", err)
	}
	events, err := store.ReadEvents()
	if err != nil || len(events) != 0 {
		t.Fatalf("ReadEvents on a fresh store = %v, err %v, want empty", events, err)
	}

	st := &State{
		Issue:     "Fix the login redirect",
		Stage:     "plan",
		Status:    "in_progress",
		SendBacks: 1,
		StartedAt: time.Now().UTC().Add(-time.Hour),
	}
	if err := store.SaveState(st); err != nil {
		t.Fatal(err)
	}
	got, err := store.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if got.Issue != st.Issue || got.Stage != "plan" || got.SendBacks != 1 {
		t.Fatalf("loaded state = %+v", got)
	}
	if got.UpdatedAt.IsZero() {
		t.Fatal("SaveState should set UpdatedAt")
	}

	if err := store.AppendEvent(Event{Stage: "intake", Event: "gate_passed", Detail: "all fields present"}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendEvent(Event{Stage: "plan", Event: "tasks_written", Detail: "3 tasks"}); err != nil {
		t.Fatal(err)
	}
	events, err = store.ReadEvents()
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("read %d events, want 2", len(events))
	}
	if events[0].Event != "gate_passed" || events[1].Event != "tasks_written" {
		t.Fatalf("events = %+v", events)
	}
	if events[0].TS == "" || events[1].TS == "" {
		t.Fatal("events should carry timestamps")
	}

	// A second save replaces the state, it does not append.
	st.Stage = "generate"
	if err := store.SaveState(st); err != nil {
		t.Fatal(err)
	}
	got, err = store.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if got.Stage != "generate" {
		t.Fatalf("stage = %q, want generate", got.Stage)
	}
	events, err = store.ReadEvents()
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("state save must not touch events, got %d", len(events))
	}
}

func TestStoreBadState(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadState(); err == nil {
		t.Fatal("expected an error decoding a corrupt state.json")
	}
}
