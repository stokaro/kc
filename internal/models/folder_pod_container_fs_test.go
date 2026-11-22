package models

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stokaro/kc/internal/podfs"
	table "github.com/stokaro/kc/internal/table"
)

type fakePodFSFactory struct {
	session podfs.ExecSession
	err     error
	block   chan struct{} // when set, NewSession waits for it to close
}

// loadedLines waits for the background load and returns the folder's rows.
func loadedLines(t *testing.T, folder *PodContainerFSFolder, n int) []table.Row {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rows := folder.Lines(t.Context(), 0, n)
		if len(rows) > 1 && rows[1].(interface{ Details() string }).Details() != "Reading "+folder.dir {
			return rows
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("folder never finished loading")
	return nil
}

func (f fakePodFSFactory) NewSession(ctx context.Context, _ podfs.SessionSpec) (podfs.ExecSession, error) {
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.session, nil
}

type fakeExecSession struct {
	entries  []podfs.FileEntry
	fileData map[string]string
}

func (f *fakeExecSession) List(context.Context, string) ([]podfs.FileEntry, error) {
	return f.entries, nil
}

func (f *fakeExecSession) ReadFile(_ context.Context, path string, limit int64) (io.ReadCloser, error) {
	if data, ok := f.fileData[path]; ok {
		if limit > 0 && int(limit) < len(data) {
			data = data[:limit]
		}
		return io.NopCloser(strings.NewReader(data)), nil
	}
	return io.NopCloser(strings.NewReader("")), nil
}

func (f *fakeExecSession) Close() error { return nil }

func TestPodContainerFSFolderListsEntries(t *testing.T) {
	ctx := t.Context()
	session := &fakeExecSession{
		entries: []podfs.FileEntry{
			{Name: "etc", Type: podfs.EntryTypeDir, Size: 0, UpdatedAt: time.Unix(1, 0)},
			{Name: "readme.txt", Type: podfs.EntryTypeFile, Size: 12, UpdatedAt: time.Unix(2, 0)},
		},
		fileData: map[string]string{"/readme.txt": "hello world\n"},
	}
	deps := Deps{
		Ctx:          ctx,
		PodFSFactory: fakePodFSFactory{session: session},
	}
	handle := newContainerSessionHandle(deps.PodFSFactory, "ns", "pod", "container")
	folder := NewPodContainerFSFolder(deps, []string{"containers", "pod", "root"}, "/", handle, false)

	rows := loadedLines(t, folder, 10)
	if len(rows) < 3 { // Back + two entries
		t.Fatalf("expected at least 3 rows, got %d", len(rows))
	}

	fileItem, ok := rows[2].(*PodFSFileItem)
	if !ok {
		t.Fatalf("expected simple item for file, got %T", rows[2])
	}
	title, body, _, _, _, err := fileItem.ViewContent()
	if err != nil {
		t.Fatalf("ViewContent error: %v", err)
	}
	if title != "/readme.txt" {
		t.Fatalf("title = %q, want /readme.txt", title)
	}
	if !strings.Contains(body, "hello world") {
		t.Fatalf("unexpected body: %q", body)
	}
}

// TestPodContainerFSFolderFileDetails covers #139: the info line read
// "8 B bytes", a size that already carries its unit followed by "bytes".
func TestPodContainerFSFolderFileDetails(t *testing.T) {
	tests := []struct {
		name string
		size int64
		want string
	}{
		{name: "small", size: 8, want: "8 bytes"},
		{name: "empty", size: 0, want: "0 bytes"},
		{name: "kibibytes", size: 1536, want: "1.5 KiB (1536 bytes)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			session := &fakeExecSession{entries: []podfs.FileEntry{
				{Name: "f", Type: podfs.EntryTypeFile, Size: tc.size, UpdatedAt: time.Unix(2, 0)},
			}}
			deps := Deps{Ctx: t.Context(), PodFSFactory: fakePodFSFactory{session: session}}
			handle := newContainerSessionHandle(deps.PodFSFactory, "ns", "pod", "container")
			folder := NewPodContainerFSFolder(deps, []string{"containers", "pod", "root"}, "/", handle, false)
			rows := loadedLines(t, folder, 10)
			if len(rows) < 2 {
				t.Fatalf("expected Back and the file, got %d rows", len(rows))
			}
			item, ok := rows[1].(*PodFSFileItem)
			if !ok {
				t.Fatalf("row 1 is %T, want *PodFSFileItem", rows[1])
			}
			if got := item.Details(); got != tc.want {
				t.Fatalf("details = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPodContainerFSFolderMissingShellMessage(t *testing.T) {
	ctx := t.Context()
	deps := Deps{
		Ctx:          ctx,
		PodFSFactory: fakePodFSFactory{err: fmt.Errorf("%w", podfs.ErrShellMissing)},
	}
	handle := newContainerSessionHandle(deps.PodFSFactory, "ns", "pod", "container")
	folder := NewPodContainerFSFolder(deps, []string{"containers", "pod", "root"}, "/", handle, false)

	rows := loadedLines(t, folder, 5)
	if len(rows) < 2 {
		t.Fatalf("expected rows")
	}
	errRow, ok := rows[1].(*SimpleItem)
	if !ok {
		t.Fatalf("expected SimpleItem, got %T", rows[1])
	}
	if !strings.Contains(errRow.Details(), "lacks /bin/sh") {
		t.Fatalf("details = %q, want mention of missing shell", errRow.Details())
	}
	title, body, _, _, _, err := errRow.ViewContent()
	if err != nil {
		t.Fatalf("ViewContent error: %v", err)
	}
	if !strings.Contains(body, "lacks /bin/sh") || !strings.Contains(title, "filesystem") {
		t.Fatalf("unexpected view content: title=%q body=%q", title, body)
	}
}

// A slow session (exec, then the debug helper) must not block the UI: the
// first call returns a loading row at once, and the rows appear when the
// background load finishes, independent of the caller's short deadline.
func TestPodContainerFSFolderLoadsInBackground(t *testing.T) {
	release := make(chan struct{})
	session := &fakeExecSession{entries: []podfs.FileEntry{{Name: "etc", Type: podfs.EntryTypeDir}}}
	deps := Deps{Ctx: t.Context(), PodFSFactory: fakePodFSFactory{session: session, block: release}}
	handle := newContainerSessionHandle(deps.PodFSFactory, "ns", "pod", "container")
	folder := NewPodContainerFSFolder(deps, []string{"containers", "pod", "root"}, "/", handle, false)

	uiCtx, cancel := context.WithTimeout(t.Context(), time.Millisecond)
	defer cancel()
	start := time.Now()
	rows := folder.Lines(uiCtx, 0, 5)
	if time.Since(start) > 100*time.Millisecond {
		t.Fatalf("Lines blocked for %v", time.Since(start))
	}
	if len(rows) != 2 || rows[1].(interface{ Details() string }).Details() != "Reading /" {
		t.Fatalf("expected a loading row, got %d rows", len(rows))
	}
	time.Sleep(20 * time.Millisecond) // past the UI deadline
	close(release)
	rows = loadedLines(t, folder, 5)
	if id := rows[1].(interface{ ID() string }).ID(); id != "/etc" {
		t.Fatalf("first entry = %q, want /etc", id)
	}
}
