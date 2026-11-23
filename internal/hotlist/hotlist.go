// Package hotlist stores the user's saved panel locations (the mc
// "directory hotlist") in a state file of its own, ~/.kc/hotlist.yaml. An
// entry is a plain bookmark of a location, or a named view: a location with
// the list settings to restore there.
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
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/stokaro/kc/internal/navigation"
)

// FileName is the hotlist file name inside ~/.kc.
const FileName = "hotlist.yaml"

// Entry is one hotlist entry. The location fields sit at the top level of
// the entry, so files written before views existed load as bookmarks.
type Entry struct {
	navigation.Location
	// Name names a saved view; bookmarks have none.
	Name string `json:"name,omitempty"`
	// View holds the list settings a saved view restores.
	View *View `json:"view,omitempty"`
}

// View is what a saved view restores on its list, besides the location.
// It holds settings only, never listed data. Empty fields leave the
// panel's current setting alone.
type View struct {
	// Columns is "normal" or "wide".
	Columns string `json:"columns,omitempty"`
	// Order is an objects order, e.g. "-mem" (see appconfig).
	Order string `json:"order,omitempty"`
	// GroupByNamespace is set for lists across namespaces only.
	GroupByNamespace *bool `json:"groupByNamespace,omitempty"`
	// Filter is the row filter text, parsed again on restore.
	Filter string `json:"filter,omitempty"`
	// FaultsOnly is the faults-only toggle of the filter.
	FaultsOnly bool `json:"faultsOnly,omitempty"`
}

// Label is what the hotlist shows for e: the view name, or the path.
func (e Entry) Label() string {
	if e.Name != "" {
		return e.Name
	}
	return e.Path
}

// file is the on-disk shape.
type file struct {
	Entries []Entry `json:"entries"`
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
func Load(path string) ([]Entry, error) {
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
func Save(path string, entries []Entry) error {
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

// Add appends a bookmark of loc unless an equal bookmark exists. It
// returns the new list and whether loc was added. Saved views at the same
// location do not count: a bookmark and a view are different entries.
func Add(entries []Entry, loc navigation.Location) ([]Entry, bool) {
	if slices.ContainsFunc(entries, func(e Entry) bool { return e.View == nil && e.Location.Equal(loc) }) {
		return entries, false
	}
	loc.Steps = slices.Clone(loc.Steps)
	return append(slices.Clip(entries), Entry{Location: loc}), true
}

// AddView appends a saved view named name at loc. Names are unique, in any
// case.
func AddView(entries []Entry, name string, loc navigation.Location, view View) ([]Entry, error) {
	name, err := checkName(entries, -1, name)
	if err != nil {
		return entries, err
	}
	loc.Steps = slices.Clone(loc.Steps)
	return append(slices.Clip(entries), Entry{Location: loc, Name: name, View: &view}), nil
}

// UpdateView replaces the location and settings of saved view i and keeps
// its name. It refuses an entry that is not a view, so a bookmark never
// turns into a view by accident.
func UpdateView(entries []Entry, i int, loc navigation.Location, view View) ([]Entry, error) {
	if i < 0 || i >= len(entries) || entries[i].View == nil {
		return entries, fmt.Errorf("hotlist: entry %d is not a saved view", i)
	}
	out := slices.Clone(entries)
	loc.Steps = slices.Clone(loc.Steps)
	out[i].Location = loc
	out[i].View = &view
	return out, nil
}

// Rename names entry i. A bookmark that gets a name stays a bookmark; an
// empty name is refused.
func Rename(entries []Entry, i int, name string) ([]Entry, error) {
	if i < 0 || i >= len(entries) {
		return entries, fmt.Errorf("hotlist: no entry %d", i)
	}
	name, err := checkName(entries, i, name)
	if err != nil {
		return entries, err
	}
	out := slices.Clone(entries)
	out[i].Name = name
	return out, nil
}

// checkName trims name and refuses an empty name or one that another entry
// than skip already has.
func checkName(entries []Entry, skip int, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("the name is empty")
	}
	for j, e := range entries {
		if j != skip && strings.EqualFold(e.Name, name) {
			return "", fmt.Errorf("a view named %q exists", e.Name)
		}
	}
	return name, nil
}

// Remove returns entries without entry i. An index out of range changes
// nothing.
func Remove(entries []Entry, i int) []Entry {
	if i < 0 || i >= len(entries) {
		return entries
	}
	return slices.Delete(slices.Clone(entries), i, i+1)
}
