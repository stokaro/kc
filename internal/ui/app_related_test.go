package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/stokaro/kc/internal/models"
	modeltesting "github.com/stokaro/kc/internal/models/testing"
	nav "github.com/stokaro/kc/internal/navigation"
	table "github.com/stokaro/kc/internal/table"
)

var podsGVRForTest = schema.GroupVersionResource{Version: "v1", Resource: "pods"}

type closeCounter struct {
	models.Folder
	closed int
}

func (c *closeCounter) Close() { c.closed++ }

func relatedTestApp(t *testing.T, rows []table.Row) (*App, models.Folder) {
	t.Helper()
	a := NewApp()
	t.Cleanup(a.cancel)
	ctx := t.Context()
	root := modeltesting.NewStaticFolder("/", nil, rows)
	a.leftNav = nav.NewNavigator(root)
	a.leftPanel.UseFolder(ctx, true)
	a.leftPanel.SetFolder(ctx, root, false)
	a.activePanel = 0
	return a, root
}

func TestHandleRelatedFolder(t *testing.T) {
	t.Parallel()
	obj := models.NewObjectRow("web", []string{"web"}, []string{"web"}, podsGVRForTest, "shop", "web", nil)
	resultRows := []table.Row{
		models.NewRowItem("apps/v1/replicasets/shop/a", []string{"a"}, nil, nil),
		models.NewRowItem("apps/v1/replicasets/shop/b", []string{"b"}, nil, nil),
	}
	cases := []struct {
		name       string
		msg        func(from models.Folder, result models.Folder) relatedFolderMsg
		changePane bool
		wantOpened bool
		wantClosed int
		wantSel    string
	}{
		{
			name: "opens in the same panel with the cursor on the first row",
			msg: func(from, result models.Folder) relatedFolderMsg {
				return relatedFolderMsg{panelIdx: 0, from: from, selID: "web", folder: result, firstID: "apps/v1/replicasets/shop/b"}
			},
			wantOpened: true,
			wantSel:    "apps/v1/replicasets/shop/b",
		},
		{
			name: "dropped and closed when the user switched panels",
			msg: func(from, result models.Folder) relatedFolderMsg {
				return relatedFolderMsg{panelIdx: 0, from: from, selID: "web", folder: result}
			},
			changePane: true,
			wantClosed: 1,
		},
		{
			name: "dropped when the panel shows another folder",
			msg: func(_, result models.Folder) relatedFolderMsg {
				return relatedFolderMsg{panelIdx: 0, from: modeltesting.NewStaticFolder("/other", nil, nil), selID: "web", folder: result}
			},
			wantClosed: 1,
		},
		{
			name: "an info message opens nothing",
			msg: func(from, _ models.Folder) relatedFolderMsg {
				return relatedFolderMsg{panelIdx: 0, from: from, info: "pods/web has no owner"}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, root := relatedTestApp(t, []table.Row{obj})
			result := &closeCounter{Folder: modeltesting.NewStaticFolder("/web/owners", nil, resultRows)}
			if tc.changePane {
				a.activePanel = 1
			}
			a.handleRelatedFolder(tc.msg(root, result))
			opened := a.leftNav.Current() == models.Folder(result)
			if opened != tc.wantOpened {
				t.Fatalf("opened = %v, want %v", opened, tc.wantOpened)
			}
			if result.closed != tc.wantClosed {
				t.Fatalf("closed %d times, want %d", result.closed, tc.wantClosed)
			}
			if !tc.wantOpened {
				return
			}
			if got := a.leftPanel.GetCurrentPath(); got != "/web/owners" {
				t.Errorf("breadcrumb = %q", got)
			}
			item, ok := a.leftPanel.SelectedNavItem(t.Context())
			if !ok {
				t.Fatalf("no selection")
			}
			if id, _, _, _ := item.Columns(); id != tc.wantSel {
				t.Errorf("cursor on %q, want %q", id, tc.wantSel)
			}
			// ".." leads back to the object.
			a.handleFolderNav(true, "", nil)
			if a.leftNav.Current() != root {
				t.Errorf("back did not return to the object list")
			}
		})
	}
}

func TestRelatedKeysNeedAnObject(t *testing.T) {
	t.Parallel()
	for _, key := range []rune{'j', 'b'} {
		a, _ := relatedTestApp(t, []table.Row{models.NewRowItem("plain", []string{"plain"}, nil, nil)})
		_, cmd := a.handleKeyMsg(tea.KeyPressMsg{Code: key, Mod: tea.ModAlt}, nil)
		if cmd == nil {
			t.Fatalf("M-%c: no command", key)
		}
		msg, ok := cmd().(showToastMsg)
		if !ok || !msg.info {
			t.Fatalf("M-%c: got %#v, want an info toast", key, msg)
		}
	}
}
