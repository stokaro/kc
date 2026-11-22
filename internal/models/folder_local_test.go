package models

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stokaro/kc/internal/podfs"
	table "github.com/stokaro/kc/internal/table"
)

// localRows returns the rows without the synthetic back row.
func localRows(t *testing.T, f *LocalDirFolder) []table.Row {
	t.Helper()
	var out []table.Row
	for _, r := range f.Lines(t.Context(), 0, f.Len(t.Context())) {
		if _, back := r.(Back); !back {
			out = append(out, r)
		}
	}
	return out
}

func TestLocalDirFolderListsEntries(t *testing.T) {
	dir := t.TempDir()
	mustWrite := func(name, content string, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite("b.txt", "hello", 0o640)
	mustWrite("a.bin", strings.Repeat("x", 2048), 0o600)
	if err := os.Symlink(filepath.Join(dir, "sub"), filepath.Join(dir, "sublink")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "b.txt"), filepath.Join(dir, "flink")); err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 1, 2, 3, 4, 0, 0, time.Local)
	if err := os.Chtimes(filepath.Join(dir, "b.txt"), when, when); err != nil {
		t.Fatal(err)
	}

	f := NewLocalDirFolder(Deps{Ctx: t.Context()}, dir)
	if got := f.DisplayPath(); got != LocalPathPrefix+dir {
		t.Fatalf("DisplayPath = %q", got)
	}
	cols := f.Columns()
	var titles []string
	for _, c := range cols {
		titles = append(titles, strings.TrimSpace(c.Title))
	}
	if strings.Join(titles, ",") != "Name,Size,Mode,Modified" {
		t.Fatalf("columns = %v", titles)
	}

	rows := localRows(t, f)
	var names []string
	for _, r := range rows {
		_, cells, _, _ := r.Columns()
		names = append(names, cells[0])
	}
	// Directories (and links to them) first, then files, each by name.
	want := []string{"/sub", "~sublink", "a.bin", "b.txt", "@flink"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("names = %v, want %v", names, want)
	}

	_, cells, _, _ := rows[3].Columns()
	if cells[1] != "5 B" || cells[2] != "-rw-r-----" || cells[3] != "2026-01-02 03:04" {
		t.Fatalf("b.txt cells = %v", cells)
	}
	_, cells, _, _ = rows[2].Columns()
	if cells[1] != "2.0 KiB" {
		t.Fatalf("a.bin size = %q", cells[1])
	}

	enter, ok := rows[0].(Enterable)
	if !ok {
		t.Fatalf("directory row %T cannot be entered", rows[0])
	}
	next, err := enter.Enter()
	if err != nil {
		t.Fatal(err)
	}
	sub, ok := next.(*LocalDirFolder)
	if !ok || sub.LocalDir() != filepath.Join(dir, "sub") {
		t.Fatalf("entered %T %v", next, next)
	}
	if _, ok := rows[1].(Enterable); !ok {
		t.Fatalf("a link to a directory must be enterable")
	}
	if _, ok := rows[4].(Enterable); ok {
		t.Fatalf("a link to a file must not be enterable")
	}
}

func TestLocalDirFolderReportsReadErrors(t *testing.T) {
	f := NewLocalDirFolder(Deps{Ctx: t.Context()}, filepath.Join(t.TempDir(), "missing"))
	rows := localRows(t, f)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1 error row", len(rows))
	}
	if d, ok := rows[0].(interface{ Details() string }); !ok || !strings.Contains(d.Details(), "missing") {
		t.Fatalf("error row = %#v", rows[0])
	}
}

func TestLocalAncestorsWalkFromRoot(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b")
	chain := LocalAncestors(Deps{Ctx: t.Context()}, dir)
	if len(chain) < 3 {
		t.Fatalf("chain = %d folders", len(chain))
	}
	if got := chain[0].LocalDir(); got != string(filepath.Separator) {
		t.Fatalf("first = %q, want the filesystem root", got)
	}
	if got := chain[len(chain)-1].LocalDir(); got != dir {
		t.Fatalf("last = %q, want %q", got, dir)
	}
	for i := 1; i < len(chain); i++ {
		if filepath.Dir(chain[i].LocalDir()) != chain[i-1].LocalDir() {
			t.Fatalf("%q does not follow %q", chain[i].LocalDir(), chain[i-1].LocalDir())
		}
	}
}

func TestPodFSRowsCarryEntriesAndTransferReusesConsent(t *testing.T) {
	session := &fakeExecSession{entries: []podfs.FileEntry{
		{Name: "etc", Type: podfs.EntryTypeDir, Mode: 0o40755},
		{Name: "motd", Type: podfs.EntryTypeFile, Mode: 0o100644, Size: 3},
	}}
	factory := &consentFactory{sess: session}
	deps := Deps{Ctx: t.Context(), PodFSFactory: factory}
	deps.ClusterKey.ContextName = "kind"
	handle := newContainerSessionHandle(t.Context(), factory, "ns", "pod", "app")
	folder := NewPodContainerFSFolder(deps, []string{"containers", "app", "root"}, "/", handle, false)

	// Without consent the copy must not add a helper.
	if _, _, err := folder.TransferSession(t.Context()); !errors.Is(err, ErrHelperConsentNeeded) {
		t.Fatalf("TransferSession without consent = %v", err)
	}
	for _, spec := range factory.specs {
		if spec.AllowHelper {
			t.Fatalf("a session was opened with AllowHelper before consent")
		}
	}

	handle.approveHelper()
	rows := loadedLines(t, folder, 5)
	var got []string
	for _, r := range rows {
		if fe, ok := r.(FSEntryItem); ok {
			got = append(got, fe.FSEntry().Name+":"+string(fe.FSEntry().Type))
		}
	}
	if strings.Join(got, ",") != "etc:dir,motd:file" {
		t.Fatalf("entries = %v", got)
	}
	opened := len(factory.specs)
	sess, release, err := folder.TransferSession(t.Context())
	if err != nil || sess != session {
		t.Fatalf("TransferSession = %v, %v; want the listing's session", sess, err)
	}
	release()
	if len(factory.specs) != opened {
		t.Fatalf("the copy opened a new session instead of reusing the listing's")
	}
	src := folder.TransferSource()
	if src.Context != "kind" || src.Namespace != "ns" || src.Pod != "pod" || src.Container != "app" || src.Dir != "/" {
		t.Fatalf("source = %+v", src)
	}
}
