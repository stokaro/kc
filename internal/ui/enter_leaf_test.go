package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/stokaro/kc/internal/models"
)

// plainLeaf is a row with nothing to view or enter.
type plainLeaf struct{ id string }

func (p plainLeaf) Columns() (string, []string, []*lipgloss.Style, bool) {
	return p.id, []string{p.id}, nil, true
}
func (p plainLeaf) Details() string { return "" }
func (p plainLeaf) Path() []string  { return []string{p.id} }

// Enter on a leaf row does what F3 does, as for a file in mc; rows that can
// be entered or run keep their own Enter.
func TestEnterOnLeafRowViews(t *testing.T) {
	logs := models.NewContainerLogItem(models.Deps{}, "logs", []string{"logs"}, nil, models.LogsSpec{Namespace: "ns", Pod: "p", Container: "c"})
	key := models.NewSimpleItem("key", []string{"ca.crt"}, nil, models.WhiteStyle()).
		WithViewContent(func() (string, string, string, string, string, error) {
			return "ca.crt", "pem", "", "text/plain", "ca.crt", nil
		})
	dir := models.NewChildResourceItem("dir", []string{"/configmaps"}, nil, func() (models.Folder, error) {
		return newStubFolder(nil), nil
	})
	for _, tc := range []struct {
		name      string
		item      models.Item
		wantView  bool
		wantEnter bool
	}{
		{"a logs row streams the log", logs, true, false},
		{"a key row shows its value", key, true, false},
		{"a folder row is entered", dir, false, true},
		{"a row without content does nothing", plainLeaf{id: "plain"}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			id, _, _, _ := tc.item.Columns()
			p := panelWith(t, newStubFolder([]models.Item{tc.item}), id)
			views := 0
			p.SetActionHandlers(PanelActionHandlers{
				PanelActionView: func(*Panel) tea.Cmd { views++; return nil },
			})
			entered := false
			p.SetFolderNavHandler(func(back bool, _ string, next models.Folder) { entered = !back && next != nil })
			p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			if got := views == 1; got != tc.wantView {
				t.Fatalf("Enter ran F3 %d times, want view %v", views, tc.wantView)
			}
			if entered != tc.wantEnter {
				t.Fatalf("Enter entered = %v, want %v", entered, tc.wantEnter)
			}
			// F3 agrees with Enter on whether there is something to view.
			if caps := p.Capabilities(ctx); caps.CanView != tc.wantView && !tc.wantEnter {
				t.Fatalf("CanView = %v, want %v", caps.CanView, tc.wantView)
			}
		})
	}
}
