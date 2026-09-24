package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"xmesh/internal/atomicfile"
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
	return s.state.Clone()
}

// View holds the read lock for the callback. The callback must not mutate or
// retain references to state, or call another Store method.
func (s *Store) View(fn func(*model.State)) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	fn(&s.state)
}

func (s *Store) Update(fn func(*model.State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	next := s.state.Clone()
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
	if s.state.Upstreams == nil {
		s.state.Upstreams = map[string]model.VMessUpstream{}
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
	if s.state.RetiredGrants == nil {
		s.state.RetiredGrants = map[string]model.RetiredGrant{}
	}
	if s.state.RetiredLinks == nil {
		s.state.RetiredLinks = map[string]model.RetiredLink{}
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
	if s.state.Updaters == nil {
		s.state.Updaters = map[string]model.Updater{}
	}
	if s.state.UpgradeTasks == nil {
		s.state.UpgradeTasks = map[string]model.UpgradeTask{}
	}
	if s.state.UpgradeBatches == nil {
		s.state.UpgradeBatches = map[string]model.UpgradeBatch{}
	}
}

func writeAtomic(path string, state model.State) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	b, err := json.Marshal(state)
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
	if err := atomicfile.Replace(tmpName, path); err != nil {
		return fmt.Errorf("replace state file: %w", err)
	}
	return nil
}
