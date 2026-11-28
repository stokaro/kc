package list

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"syscall"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	kccluster "github.com/stokaro/kc/internal/cluster"
	models "github.com/stokaro/kc/internal/models"
	table "github.com/stokaro/kc/internal/table"
	panelcontent "github.com/stokaro/kc/internal/ui/panelcontent"
)

// problemRowsFolder serves fixed rows and says why it lists no others.
type problemRowsFolder struct {
	rowsFolder
	problem error
}

func (f *problemRowsFolder) ListProblem() error { return f.problem }

var forbiddenConfigMaps = apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, "",
	errors.New(`User "limited" cannot list resource "configmaps" in API group "" in the namespace "team-a"`))

// renderLines draws w at width x height and returns its lines without
// styles.
func renderLines(ctx context.Context, w *Widget, width, height int) []string {
	v := w.View(ctx, panelcontent.Frame{Size: panelcontent.Size{Width: width, Height: height}})
	return strings.Split(ansi.Strip(v.Content), "\n")
}

func backRow() table.Row {
	return models.NewSimpleItem("..", []string{"/.."}, nil, models.WhiteStyle())
}

func folderWidget(t *testing.T, f models.Folder, width, height int) *Widget {
	t.Helper()
	w := New(panelcontent.WidgetDeps{})
	w.UseFolder(true)
	_, _ = w.Update(t.Context(), tea.WindowSizeMsg{Width: width, Height: height})
	w.SetFolder(t.Context(), f, false)
	return w
}

func TestFolderNoticeIsDrawnBelowTheRows(t *testing.T) {
	const width, height = 39, 10
	ctx := t.Context()
	w := folderWidget(t, &problemRowsFolder{rows: []table.Row{backRow()}, problem: forbiddenConfigMaps}, width, height)
	lines := renderLines(ctx, w, width, height)
	if len(lines) != height {
		t.Fatalf("view has %d lines, want %d:\n%s", len(lines), height, strings.Join(lines, "\n"))
	}
	for i, l := range lines {
		if got := ansi.StringWidth(l); got != width {
			t.Errorf("line %d is %d cells wide, want %d: %q", i, got, width, l)
		}
	}
	if !strings.Contains(lines[1], "/..") {
		t.Errorf("line 1 = %q, want the back row above the notice", lines[1])
	}
	// The notice starts on the line after the last row and wraps at the
	// panel's width instead of being cut.
	var text []string
	for _, l := range lines[2:] {
		text = append(text, strings.TrimSpace(l))
	}
	joined := strings.Join(strings.Fields(strings.Join(text, " ")), " ")
	want := "Not allowed to list configmaps in team-a (user limited) " + hintForbidden
	if !strings.HasPrefix(joined, want) {
		t.Errorf("notice reads %q, want it to start with %q:\n%s", joined, want, strings.Join(lines, "\n"))
	}
	if !strings.HasPrefix(strings.TrimSpace(lines[2]), "Not allowed") {
		t.Errorf("line 2 = %q, want the notice right below the rows", lines[2])
	}
}

// Rows that fill the panel leave no line for a notice; the rows are drawn
// as they are.
func TestFolderNoticeYieldsToRows(t *testing.T) {
	const width, height = 39, 6
	ctx := t.Context()
	var rows []table.Row
	for i := range height {
		rows = append(rows, models.NewSimpleItem(fmt.Sprint(i), []string{fmt.Sprintf("/row-%d", i)}, nil, models.WhiteStyle()))
	}
	w := folderWidget(t, &problemRowsFolder{rows: rows, problem: forbiddenConfigMaps}, width, height)
	lines := renderLines(ctx, w, width, height)
	view := strings.Join(lines, "\n")
	if strings.Contains(view, "Not allowed") {
		t.Errorf("a notice was drawn over rows:\n%s", view)
	}
	if !strings.Contains(lines[height-1], "/row-") {
		t.Errorf("last line = %q, want a row", lines[height-1])
	}
}

func TestFolderWithoutProblemDrawsNoNotice(t *testing.T) {
	ctx := t.Context()
	w := folderWidget(t, &problemRowsFolder{rows: []table.Row{backRow()}}, 39, 10)
	view := strings.Join(renderLines(ctx, w, 39, 10), "\n")
	for _, hint := range []string{hintConnection, hintForbidden, hintNamespace, hintRetry} {
		if strings.Contains(view, hint) {
			t.Errorf("a list without a problem shows %q:\n%s", hint, view)
		}
	}
}

// Without a folder, as before kc has a connection, the list says why
// instead of "No items".
func TestNoticeReplacesNoItems(t *testing.T) {
	const width, height = 30, 8
	ctx := t.Context()
	w := New(panelcontent.WidgetDeps{})
	w.SetNotice(Notice{Problem: "Cannot reach 127.0.0.1:6443: connection refused", Hint: hintConnection})
	lines := renderLines(ctx, w, width, height)
	view := strings.Join(lines, "\n")
	if strings.Contains(view, "No items") {
		t.Errorf("the list says No items over its notice:\n%s", view)
	}
	if got := strings.TrimSpace(lines[0]); got != "Cannot reach 127.0.0.1:6443:" {
		t.Errorf("line 0 = %q, want the notice wrapped at the width", got)
	}
	if !strings.Contains(view, "connection refused") {
		t.Errorf("the notice lost its cause:\n%s", view)
	}

	w.SetNotice(Notice{})
	if view := strings.Join(renderLines(ctx, w, width, height), "\n"); !strings.Contains(view, "No items") {
		t.Errorf("a cleared notice does not bring back No items:\n%s", view)
	}
}

func TestNoticeFor(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantProblem string
		wantHint    string
		wantPending bool
	}{
		{name: "nil", err: nil},
		{
			name:        "refused",
			err:         &url.Error{Op: "Get", URL: "https://127.0.0.1:6443/api", Err: &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}},
			wantProblem: "Cannot reach 127.0.0.1:6443: connection refused",
			wantHint:    hintConnection,
		},
		{name: "forbidden", err: forbiddenConfigMaps, wantProblem: "Not allowed to list configmaps in team-a (user limited)", wantHint: hintForbidden},
		{
			name:        "missing namespace",
			err:         apierrors.NewNotFound(schema.GroupResource{Resource: "namespaces"}, "shpo"),
			wantProblem: "Namespace shpo does not exist",
			wantHint:    hintNamespace,
		},
		{name: "discovery pending", err: fmt.Errorf("pods: %w", kccluster.ErrDiscoveryPending), wantProblem: "Reading the API discovery", wantPending: true},
		{name: "other", err: errors.New("boom"), wantProblem: "Boom", wantHint: hintRetry},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			n := NoticeFor(tt.err)
			if !strings.Contains(n.Problem, tt.wantProblem) || (tt.wantProblem == "") != (n.Problem == "") {
				t.Errorf("Problem = %q, want it to contain %q", n.Problem, tt.wantProblem)
			}
			if n.Hint != tt.wantHint {
				t.Errorf("Hint = %q, want %q", n.Hint, tt.wantHint)
			}
			if n.Pending != tt.wantPending {
				t.Errorf("Pending = %t, want %t", n.Pending, tt.wantPending)
			}
		})
	}
}
