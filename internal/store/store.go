package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"xmesh/internal/model"
)

type Store struct {
	mu    sync.RWMutex
	path  string
	state model.State
}

func Open(path string) (*Store, error) {
	s := &Store{path: path, state: model.NewState()}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read state: %w", err)
	}
	if err := json.Unmarshal(b, &s.state); err != nil {
		return nil, fmt.Errorf("decode state: %w", err)
	}
	s.ensureMaps()
	return s, nil
}

func (s *Store) Snapshot() model.State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, _ := json.Marshal(s.state)
	var copy model.State
	_ = json.Unmarshal(b, &copy)
	return copy
}

func (s *Store) Update(fn func(*model.State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	b, err := json.Marshal(s.state)
	if err != nil {
		return fmt.Errorf("copy state: %w", err)
	}
	var next model.State
	if err := json.Unmarshal(b, &next); err != nil {
		return fmt.Errorf("copy state: %w", err)
	}
	if err := fn(&next); err != nil {
		return err
	}
	next.Revision++
	if err := writeAtomic(s.path, next); err != nil {
		return err
	}
	s.state = next
	return nil
}

func (s *Store) ensureMaps() {
	if s.state.Users == nil {
		s.state.Users = map[string]model.User{}
	}
	if s.state.Gateways == nil {
		s.state.Gateways = map[string]model.Gateway{}
	}
	if s.state.Agents == nil {
		s.state.Agents = map[string]model.Agent{}
	}
	if s.state.Attachments == nil {
		s.state.Attachments = map[string]model.Attachment{}
	}
	if s.state.Links == nil {
		s.state.Links = map[string]model.Link{}
	}
	if s.state.Grants == nil {
		s.state.Grants = map[string]model.Grant{}
	}
	if s.state.Enrollments == nil {
		s.state.Enrollments = map[string]model.Enrollment{}
	}
	if s.state.NodeStatus == nil {
		s.state.NodeStatus = map[string]model.NodeStatus{}
	}
	if s.state.LinkStatus == nil {
		s.state.LinkStatus = map[string]model.LinkStatus{}
	}
	if s.state.GrantStatus == nil {
		s.state.GrantStatus = map[string]model.GrantStatus{}
	}
}

func writeAtomic(path string, state model.State) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".xmesh-state-*")
	if err != nil {
		return fmt.Errorf("create state temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("protect state temp file: %w", err)
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write state temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync state temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close state temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err == nil {
		return nil
	} else if runtime.GOOS != "windows" {
		return fmt.Errorf("replace state file: %w", err)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove previous state file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace state file: %w", err)
	}
	return nil
}
