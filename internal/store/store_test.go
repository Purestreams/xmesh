package store

import (
	"errors"
	"path/filepath"
	"testing"

	"xmesh/internal/model"
)

func TestUpdateIsAtomicOnCallbackError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Update(func(state *model.State) error {
		state.Users["kept"] = model.User{ID: "kept"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("no")
	if err := s.Update(func(state *model.State) error {
		delete(state.Users, "kept")
		return wantErr
	}); !errors.Is(err, wantErr) {
		t.Fatalf("got %v, want %v", err, wantErr)
	}
	if _, ok := s.Snapshot().Users["kept"]; !ok {
		t.Fatal("failed update mutated live state")
	}
	if err := s.Update(func(state *model.State) error {
		state.Users["second"] = model.User{ID: "second"}
		return nil
	}); err != nil {
		t.Fatalf("replace existing state: %v", err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reopened.Snapshot().Users["kept"]; !ok {
		t.Fatal("persisted state lost committed user")
	}
	if _, ok := reopened.Snapshot().Users["second"]; !ok {
		t.Fatal("persisted state lost replacement update")
	}
}
