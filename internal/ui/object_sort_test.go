package ui

import (
	"testing"
)

func TestNextAndReverseSortOrder(t *testing.T) {
	t.Parallel()
	keys := []string{"name", "namespace", "ready", "restarts", "age"}
	cases := []struct {
		name    string
		next    func([]string, string) string
		applied string
		want    string
	}{
		{"next column ascending", nextSortOrder, "ready", "restarts"},
		{"next from descending starts ascending", nextSortOrder, "-ready", "restarts"},
		{"wraps after the last column", nextSortOrder, "-age", "name"},
		{"unknown key starts at the first column", nextSortOrder, "ip", "name"},
		{"reverse ascending", reverseSortOrder, "age", "-age"},
		{"reverse descending", reverseSortOrder, "-age", "age"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.next(keys, tc.applied); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
	if got := nextSortOrder(nil, "age"); got != "age" {
		t.Errorf("no columns: got %q, want the order unchanged", got)
	}
}

// objectsDialog opens View Options with the Objects section only and moves
// the focus to opt.
func objectsDialog(t *testing.T, cfg ViewOptionsObjectsConfig, opt viewOptionKind) *ViewOptionsModel {
	t.Helper()
	m := NewViewOptionsModel(ViewOptionsConfig{SkipPanelSection: true, Objects: &cfg})
	for i, e := range m.entries {
		if e.kind == viewOptionEntryOption && e.option == opt {
			m.focus = i
			return m
		}
	}
	t.Fatalf("option %d not in the dialog", opt)
	return nil
}

func TestViewOptionsObjectsSortBy(t *testing.T) {
	t.Parallel()
	cfg := ViewOptionsObjectsConfig{
		Order:      "-age",
		SortKeys:   []string{"name", "ready", "age"},
		SortTitles: []string{"Name", "Ready", "Age"},
	}
	t.Run("shows the applied column and direction", func(t *testing.T) {
		t.Parallel()
		m := objectsDialog(t, cfg, viewOptionObjectOrder)
		if _, v := m.optionLabelAndValue(viewOptionObjectOrder); v != "Age" {
			t.Errorf("Sort by = %q, want Age", v)
		}
		if _, v := m.optionLabelAndValue(viewOptionObjectReverse); v != "Yes" {
			t.Errorf("Reverse = %q, want Yes", v)
		}
		if got := m.commit(true, false).Objects.Order; got != "-age" {
			t.Errorf("unchanged dialog commits %q, want -age", got)
		}
	})
	t.Run("cycling the column keeps the direction", func(t *testing.T) {
		t.Parallel()
		m := objectsDialog(t, cfg, viewOptionObjectOrder)
		m.adjustCurrent(1) // wraps to Name
		m.adjustCurrent(1)
		if got := m.commit(true, false).Objects.Order; got != "-ready" {
			t.Errorf("order = %q, want -ready", got)
		}
		m.adjustCurrent(-1)
		if got := m.commit(true, false).Objects.Order; got != "-name" {
			t.Errorf("order = %q, want -name", got)
		}
	})
	t.Run("reverse toggles the direction", func(t *testing.T) {
		t.Parallel()
		m := objectsDialog(t, cfg, viewOptionObjectReverse)
		m.adjustCurrent(0)
		if got := m.commit(true, false).Objects.Order; got != "age" {
			t.Errorf("order = %q, want age", got)
		}
	})
	t.Run("creation reads as age reversed", func(t *testing.T) {
		t.Parallel()
		c := cfg
		c.Order = "creation"
		m := objectsDialog(t, c, viewOptionObjectOrder)
		if got := m.commit(true, false).Objects.Order; got != "-age" {
			t.Errorf("order = %q, want -age", got)
		}
	})
	t.Run("a column the list does not show stays selectable", func(t *testing.T) {
		t.Parallel()
		c := cfg
		c.Order = "ip"
		m := objectsDialog(t, c, viewOptionObjectOrder)
		if got := m.commit(true, false).Objects.Order; got != "ip" {
			t.Errorf("order = %q, want ip", got)
		}
	})
	t.Run("without columns only name", func(t *testing.T) {
		t.Parallel()
		m := objectsDialog(t, ViewOptionsObjectsConfig{Order: "-name"}, viewOptionObjectOrder)
		m.adjustCurrent(1)
		if got := m.commit(true, false).Objects.Order; got != "-name" {
			t.Errorf("order = %q, want -name", got)
		}
	})
}
