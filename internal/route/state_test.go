package route

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestNewStateStore(t *testing.T) {
	store := NewStateStore("/tmp/test-state.json")

	if store == nil {
		t.Fatal("NewStateStore() returned nil")
	}
	if store.Path() != "/tmp/test-state.json" {
		t.Errorf("Path() = %s, want /tmp/test-state.json", store.Path())
	}
}

func TestStateStore_SetPath(t *testing.T) {
	store := NewStateStore("/tmp/original.json")

	store.SetPath("/tmp/updated.json")

	if store.Path() != "/tmp/updated.json" {
		t.Errorf("Path() = %s, want /tmp/updated.json", store.Path())
	}
}

func TestStateStore_Path(t *testing.T) {
	store := NewStateStore("/etc/mutiauk/state.json")

	path := store.Path()

	if path != "/etc/mutiauk/state.json" {
		t.Errorf("Path() = %s, want /etc/mutiauk/state.json", path)
	}
}

func TestStateStore_Load_NonExistentFile(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewStateStore(filepath.Join(tmpDir, "nonexistent.json"))

	state, err := store.Load()

	if err != nil {
		t.Errorf("Load() error = %v, want nil for non-existent file", err)
	}
	if state == nil {
		t.Error("Load() returned nil state, want empty state")
	}
	if len(state) != 0 {
		t.Errorf("Load() returned state with %d entries, want 0", len(state))
	}
}

func TestStateStore_Load_ValidFile(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")

	// First save a state using the StateStore to get correct format
	store := NewStateStore(stateFile)
	route := Route{
		Destination: mustParseCIDR("10.0.0.0/8"),
		Interface:   "tun0",
		Comment:     "Test route",
		Enabled:     true,
	}
	if err := store.AddRoute(route); err != nil {
		t.Fatalf("AddRoute() error = %v", err)
	}

	// Now load and verify
	state, err := store.Load()

	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(state) != 1 {
		t.Errorf("Load() returned %d entries, want 1", len(state))
	}
	if r, ok := state["10.0.0.0/8"]; !ok {
		t.Error("state missing 10.0.0.0/8 route")
	} else {
		if r.Interface != "tun0" {
			t.Errorf("route.Interface = %s, want tun0", r.Interface)
		}
		if r.Comment != "Test route" {
			t.Errorf("route.Comment = %s, want 'Test route'", r.Comment)
		}
	}
}

func TestStateStore_Load_InvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "invalid.json")

	// Create an invalid JSON file
	if err := os.WriteFile(stateFile, []byte("not valid json {{{"), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	store := NewStateStore(stateFile)
	_, err := store.Load()

	if err == nil {
		t.Error("Load() expected error for invalid JSON, got nil")
	}
}

func TestStateStore_Load_EmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "empty.json")

	// Create an empty file
	if err := os.WriteFile(stateFile, []byte(""), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	store := NewStateStore(stateFile)
	_, err := store.Load()

	// Empty file is invalid JSON
	if err == nil {
		t.Error("Load() expected error for empty file, got nil")
	}
}

func TestStateStore_Load_NullJSON(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "null.json")

	// Create a file with null JSON
	if err := os.WriteFile(stateFile, []byte("null"), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	store := NewStateStore(stateFile)
	state, err := store.Load()

	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	// null should be converted to empty state
	if state == nil {
		t.Error("Load() returned nil state, want empty state")
	}
}

func TestStateStore_Load_EmptyObject(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "empty-obj.json")

	// Create a file with empty object
	if err := os.WriteFile(stateFile, []byte("{}"), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	store := NewStateStore(stateFile)
	state, err := store.Load()

	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(state) != 0 {
		t.Errorf("Load() returned %d entries, want 0", len(state))
	}
}

func TestStateStore_Save(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")

	store := NewStateStore(stateFile)
	state := State{
		"10.0.0.0/8": Route{
			Interface: "tun0",
			Comment:   "Test",
			Enabled:   true,
		},
	}

	err := store.Save(state)

	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Verify file was created
	if _, err := os.Stat(stateFile); os.IsNotExist(err) {
		t.Error("Save() did not create state file")
	}

	// Verify content
	data, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatalf("failed to read state file: %v", err)
	}

	var loaded State
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("failed to parse saved state: %v", err)
	}

	if len(loaded) != 1 {
		t.Errorf("saved state has %d entries, want 1", len(loaded))
	}
}

func TestStateStore_Save_CreatesDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "subdir", "nested", "state.json")

	store := NewStateStore(stateFile)
	state := State{}

	err := store.Save(state)

	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Verify directories were created
	if _, err := os.Stat(filepath.Dir(stateFile)); os.IsNotExist(err) {
		t.Error("Save() did not create parent directories")
	}
}

func TestStateStore_Save_EmptyState(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "empty-state.json")

	store := NewStateStore(stateFile)
	state := State{}

	err := store.Save(state)

	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	data, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatalf("failed to read state file: %v", err)
	}

	// Empty state should be "{}"
	var loaded State
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("failed to parse saved state: %v", err)
	}
	if len(loaded) != 0 {
		t.Errorf("loaded state has %d entries, want 0", len(loaded))
	}
}

func TestStateStore_Save_Overwrites(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")

	store := NewStateStore(stateFile)

	// Save initial state
	state1 := State{
		"10.0.0.0/8": Route{Interface: "tun0"},
	}
	if err := store.Save(state1); err != nil {
		t.Fatalf("first Save() error = %v", err)
	}

	// Save new state
	state2 := State{
		"192.168.0.0/16": Route{Interface: "tun1"},
	}
	if err := store.Save(state2); err != nil {
		t.Fatalf("second Save() error = %v", err)
	}

	// Load and verify
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if _, ok := loaded["10.0.0.0/8"]; ok {
		t.Error("old route should not exist after overwrite")
	}
	if _, ok := loaded["192.168.0.0/16"]; !ok {
		t.Error("new route should exist after overwrite")
	}
}

func TestStateStore_AddRoute(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")

	store := NewStateStore(stateFile)
	route := Route{
		Destination: mustParseCIDR("10.0.0.0/8"),
		Interface:   "tun0",
		Comment:     "Test route",
		Enabled:     false, // Should be set to true by AddRoute
	}

	err := store.AddRoute(route)

	if err != nil {
		t.Fatalf("AddRoute() error = %v", err)
	}

	// Verify route was added
	state, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if r, ok := state["10.0.0.0/8"]; !ok {
		t.Error("route was not added to state")
	} else {
		if !r.Enabled {
			t.Error("AddRoute() should set Enabled to true")
		}
		if r.Comment != "Test route" {
			t.Errorf("Comment = %s, want 'Test route'", r.Comment)
		}
	}
}

func TestStateStore_AddRoute_Multiple(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")

	store := NewStateStore(stateFile)

	routes := []Route{
		{Destination: mustParseCIDR("10.0.0.0/8"), Interface: "tun0"},
		{Destination: mustParseCIDR("192.168.0.0/16"), Interface: "tun0"},
		{Destination: mustParseCIDR("172.16.0.0/12"), Interface: "tun0"},
	}

	for _, r := range routes {
		if err := store.AddRoute(r); err != nil {
			t.Fatalf("AddRoute() error = %v", err)
		}
	}

	state, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if len(state) != 3 {
		t.Errorf("state has %d entries, want 3", len(state))
	}
}

func TestStateStore_AddRoute_UpdatesExisting(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")

	store := NewStateStore(stateFile)

	// Add initial route
	route1 := Route{
		Destination: mustParseCIDR("10.0.0.0/8"),
		Interface:   "tun0",
		Comment:     "Original",
	}
	if err := store.AddRoute(route1); err != nil {
		t.Fatalf("first AddRoute() error = %v", err)
	}

	// Add route with same destination but different comment
	route2 := Route{
		Destination: mustParseCIDR("10.0.0.0/8"),
		Interface:   "tun1",
		Comment:     "Updated",
	}
	if err := store.AddRoute(route2); err != nil {
		t.Fatalf("second AddRoute() error = %v", err)
	}

	state, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if len(state) != 1 {
		t.Errorf("state has %d entries, want 1 (no duplicates)", len(state))
	}

	if r := state["10.0.0.0/8"]; r.Comment != "Updated" {
		t.Errorf("Comment = %s, want 'Updated'", r.Comment)
	}
}

func TestStateStore_RemoveRoute(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")

	store := NewStateStore(stateFile)

	// Add a route first
	route := Route{
		Destination: mustParseCIDR("10.0.0.0/8"),
		Interface:   "tun0",
	}
	if err := store.AddRoute(route); err != nil {
		t.Fatalf("AddRoute() error = %v", err)
	}

	// Remove the route
	err := store.RemoveRoute(route)
	if err != nil {
		t.Fatalf("RemoveRoute() error = %v", err)
	}

	// Verify removal
	state, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if _, ok := state["10.0.0.0/8"]; ok {
		t.Error("route should have been removed from state")
	}
}

func TestStateStore_RemoveRoute_NonExistent(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")

	store := NewStateStore(stateFile)

	// Remove a route that doesn't exist
	route := Route{
		Destination: mustParseCIDR("10.0.0.0/8"),
	}
	err := store.RemoveRoute(route)

	// Should not error
	if err != nil {
		t.Fatalf("RemoveRoute() error = %v, want nil for non-existent route", err)
	}
}

func TestStateStore_RemoveRoute_LeavesOthers(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")

	store := NewStateStore(stateFile)

	// Add multiple routes
	routes := []Route{
		{Destination: mustParseCIDR("10.0.0.0/8"), Interface: "tun0"},
		{Destination: mustParseCIDR("192.168.0.0/16"), Interface: "tun0"},
	}
	for _, r := range routes {
		if err := store.AddRoute(r); err != nil {
			t.Fatalf("AddRoute() error = %v", err)
		}
	}

	// Remove only one
	if err := store.RemoveRoute(routes[0]); err != nil {
		t.Fatalf("RemoveRoute() error = %v", err)
	}

	state, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if len(state) != 1 {
		t.Errorf("state has %d entries, want 1", len(state))
	}
	if _, ok := state["192.168.0.0/16"]; !ok {
		t.Error("remaining route should still exist")
	}
}

func TestStateStore_Concurrency(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")

	store := NewStateStore(stateFile)

	var wg sync.WaitGroup
	errCh := make(chan error, 100)

	// Concurrent reads
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.Load()
			if err != nil {
				errCh <- err
			}
		}()
	}

	// Concurrent writes
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			state := State{
				"10.0.0.0/8": Route{Interface: "tun0"},
			}
			if err := store.Save(state); err != nil {
				errCh <- err
			}
		}(i)
	}

	// Concurrent path reads
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = store.Path()
		}()
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("concurrent operation error: %v", err)
	}
}

func TestStateStore_LoadSaveRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")

	store := NewStateStore(stateFile)

	original := State{
		"10.0.0.0/8": Route{
			Interface: "tun0",
			Comment:   "Private A",
			Enabled:   true,
			Metric:    100,
		},
		"192.168.0.0/16": Route{
			Interface: "tun0",
			Comment:   "Private C",
			Enabled:   false,
			Metric:    200,
		},
	}

	// Save
	if err := store.Save(original); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Load
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	// Compare
	if len(loaded) != len(original) {
		t.Errorf("loaded state has %d entries, want %d", len(loaded), len(original))
	}

	for key, origRoute := range original {
		loadedRoute, ok := loaded[key]
		if !ok {
			t.Errorf("loaded state missing key %s", key)
			continue
		}
		if loadedRoute.Interface != origRoute.Interface {
			t.Errorf("key %s: Interface = %s, want %s", key, loadedRoute.Interface, origRoute.Interface)
		}
		if loadedRoute.Comment != origRoute.Comment {
			t.Errorf("key %s: Comment = %s, want %s", key, loadedRoute.Comment, origRoute.Comment)
		}
		if loadedRoute.Enabled != origRoute.Enabled {
			t.Errorf("key %s: Enabled = %v, want %v", key, loadedRoute.Enabled, origRoute.Enabled)
		}
		if loadedRoute.Metric != origRoute.Metric {
			t.Errorf("key %s: Metric = %d, want %d", key, loadedRoute.Metric, origRoute.Metric)
		}
	}
}

func TestStateStore_IPv6Routes(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")

	store := NewStateStore(stateFile)

	route := Route{
		Destination: mustParseCIDR("2001:db8::/32"),
		Interface:   "tun0",
		Comment:     "IPv6 route",
	}

	if err := store.AddRoute(route); err != nil {
		t.Fatalf("AddRoute() error = %v", err)
	}

	state, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if _, ok := state["2001:db8::/32"]; !ok {
		t.Error("IPv6 route should exist in state")
	}
}
