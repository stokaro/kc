package navigation

import (
	"slices"
	"strings"
	"testing"

	"github.com/stokaro/kc/internal/models"
	modeltesting "github.com/stokaro/kc/internal/models/testing"
	table "github.com/stokaro/kc/internal/table"
)

// helper to make a trivial folder with a path and one name column
func mkFolder(path []string, names ...string) models.Folder {
	rows := make([]table.Row, 0, len(names))
	base := append([]string(nil), path...)
	for _, n := range names {
		rows = append(rows, models.NewSimpleItem(n, []string{n}, append(append([]string(nil), base...), n), models.WhiteStyle()))
	}
	title := "/"
	if len(path) > 0 {
		title = strings.Join(path, "/")
	}
	return modeltesting.NewStaticFolder(title, []table.Column{{Title: " Name"}}, rows)
}

func TestNavigator_BackFromRootNamespacesGoesToRoot(t *testing.T) {
	root := mkFolder(nil, "contexts", "namespaces")
	ns := mkFolder([]string{"namespaces"}, "default", "kube-system")

	nav := NewNavigator(root)
	if nav.HasBack() {
		t.Fatalf("unexpected back available at root")
	}
	nav.Push(ns)
	if !nav.HasBack() {
		t.Fatalf("expected back after entering namespaces")
	}
	cur := nav.Back()
	if cur == nil || len(cur.Path()) != 0 {
		t.Fatalf("expected root path empty, got %v", cur.Path())
	}
}

func TestNavigator_BackFromContextNamespacesGoesToContexts(t *testing.T) {
	root := mkFolder(nil, "contexts")
	contexts := mkFolder([]string{"contexts"}, "ctxA", "ctxB")
	ctxNamespaces := mkFolder([]string{"contexts", "ctxA", "namespaces"}, "default")

	nav := NewNavigator(root)
	nav.Push(contexts)
	if !nav.HasBack() {
		t.Fatalf("expected back after entering contexts")
	}
	nav.Push(ctxNamespaces)
	if !nav.HasBack() {
		t.Fatalf("expected back after entering ctx namespaces")
	}
	if cur := nav.Back(); cur == nil || !equalPath(cur.Path(), []string{"contexts"}) {
		t.Fatalf("expected to be back at contexts, got %v", cur.Path())
	}
}

func equalPath(a, b []string) bool {
	return slices.Equal(a, b)
}

// closingFolder counts Close calls.
type closingFolder struct {
	models.Folder
	closed int
}

func (c *closingFolder) Close() { c.closed++ }

func TestNavigatorClosesDiscardedFolders(t *testing.T) {
	root := &closingFolder{Folder: mkFolder(nil, "a")}
	child := &closingFolder{Folder: mkFolder([]string{"a"}, "b")}
	grand := &closingFolder{Folder: mkFolder([]string{"a", "b"}, "c")}

	nav := NewNavigator(root)
	nav.Push(child)
	nav.Push(grand)
	nav.Back()
	if grand.closed != 1 || child.closed != 0 || root.closed != 0 {
		t.Fatalf("Back must close only the popped folder: root=%d child=%d grand=%d", root.closed, child.closed, grand.closed)
	}
	nav.Close()
	if child.closed != 1 || root.closed != 1 || grand.closed != 1 {
		t.Fatalf("Close must close the rest once: root=%d child=%d grand=%d", root.closed, child.closed, grand.closed)
	}
}

func TestNavigator_Steps(t *testing.T) {
	root := mkFolder(nil, "pods")
	pods := mkFolder([]string{"pods"}, "nginx")
	nginx := mkFolder([]string{"pods", "nginx"}, "containers")

	nav := NewNavigator(root)
	if got := nav.Steps(); got != nil {
		t.Fatalf("Steps at root = %v, want nil", got)
	}
	nav.SetSelectionID("pods")
	nav.Push(pods)
	nav.SetSelectionID("nginx")
	nav.Push(nginx)
	nav.SetSelectionID("containers") // the cursor, not an entered row
	if got := strings.Join(nav.Steps(), ","); got != "pods,nginx" {
		t.Fatalf("Steps = %q, want pods,nginx", got)
	}
	nav.Back()
	if got := strings.Join(nav.Steps(), ","); got != "pods" {
		t.Fatalf("Steps after Back = %q, want pods", got)
	}
}
