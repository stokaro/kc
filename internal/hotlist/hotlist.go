// Package hotlist stores the user's saved panel locations (the mc
// "directory hotlist") in a state file of its own, ~/.kc/hotlist.yaml.
//
// The file is kc state, not configuration: kc rewrites it whenever an entry
// is added or removed, so it is kept apart from ~/.kc/config.yaml and never
// competes with config saves.
package hotlist

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"sigs.k8s.io/yaml"

	"github.com/stokaro/kc/internal/navigation"
)

// FileName is the hotlist file name inside ~/.kc.
const FileName = "hotlist.yaml"

// file is the on-disk shape.
type file struct {
	Entries []navigation.Location `json:"entries"`
}

// DefaultPath returns ~/.kc/hotlist.yaml.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("hotlist: home directory: %w", err)
	}
	return filepath.Join(home, ".kc", FileName), nil
}

// Load reads the entries from path. A missing file is an empty hotlist.
func Load(path string) ([]navigation.Location, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("hotlist: reading %s: %w", path, err)
	}
	var f file
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("hotlist: parsing %s: %w", path, err)
	}
	return f.Entries, nil
}

// Save writes entries to path. It writes a temporary file in the same
// directory and renames it over path, so a crash never leaves a half
// written hotlist. The file is private to the user (0600), since entries
// name contexts and namespaces.
func Save(path string, entries []navigation.Location) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("hotlist: creating %s: %w", dir, err)
	}
	data, err := yaml.Marshal(file{Entries: entries})
	if err != nil {
		return fmt.Errorf("hotlist: encoding: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "."+FileName+".*")
	if err != nil {
		return fmt.Errorf("hotlist: creating temporary file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		// Removes the temporary file when the rename did not happen.
		_ = os.Remove(tmpName)
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("hotlist: chmod %s: %w", tmpName, err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("hotlist: writing %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("hotlist: syncing %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("hotlist: closing %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("hotlist: replacing %s: %w", path, err)
	}
	return nil
}

// Add appends loc unless an equal entry exists. It returns the new list and
// whether loc was added.
func Add(entries []navigation.Location, loc navigation.Location) ([]navigation.Location, bool) {
	if slices.ContainsFunc(entries, loc.Equal) {
		return entries, false
	}
	loc.Steps = slices.Clone(loc.Steps)
	return append(slices.Clip(entries), loc), true
}

// Remove returns entries without entry i. An index out of range changes
// nothing.
func Remove(entries []navigation.Location, i int) []navigation.Location {
	if i < 0 || i >= len(entries) {
		return entries
	}
	return slices.Delete(slices.Clone(entries), i, i+1)
}
