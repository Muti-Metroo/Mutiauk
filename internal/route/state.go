package route

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// State represents saved route state as a map from destination CIDR to Route.
type State map[string]Route

// StateStore handles route state persistence to a JSON file.
type StateStore struct {
	mu   sync.RWMutex
	path string
}

// NewStateStore creates a new state store with the given file path.
func NewStateStore(path string) *StateStore {
	return &StateStore{path: path}
}

// SetPath updates the state file path.
func (s *StateStore) SetPath(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.path = path
}

// Path returns the current state file path.
func (s *StateStore) Path() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.path
}

// Load reads the state from the file. Returns an empty state if the file does not exist.
func (s *StateStore) Load() (State, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return make(State), nil
		}
		return nil, fmt.Errorf("failed to read state file: %w", err)
	}

	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("failed to parse state file: %w", err)
	}

	if state == nil {
		state = make(State)
	}
	return state, nil
}

// Save writes the state to the file.
func (s *StateStore) Save(state State) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create state directory: %w", err)
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal state: %w", err)
	}

	if err := os.WriteFile(s.path, data, 0644); err != nil {
		return fmt.Errorf("failed to write state file: %w", err)
	}

	return nil
}

// AddRoute adds a route to the persisted state.
func (s *StateStore) AddRoute(r Route) error {
	state, err := s.Load()
	if err != nil {
		return err
	}

	key := r.Destination.String()
	r.Enabled = true
	state[key] = r

	return s.Save(state)
}

// RemoveRoute removes a route from the persisted state.
func (s *StateStore) RemoveRoute(r Route) error {
	state, err := s.Load()
	if err != nil {
		return err
	}

	key := r.Destination.String()
	delete(state, key)

	return s.Save(state)
}
