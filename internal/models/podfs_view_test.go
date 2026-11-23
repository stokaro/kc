package models

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/stokaro/kc/internal/pagedfile"
	"github.com/stokaro/kc/internal/podfs"
)

// rangeExecSession is a filesystem session that serves one file by range
// and records each range it is asked for, as the dd command would get it.
type rangeExecSession struct {
	fakeExecSession
	data []byte

	mu     sync.Mutex
	ranges [][2]int64
}

func (s *rangeExecSession) ReadFileRange(_ context.Context, _ string, offset, length int64) (io.ReadCloser, int64, error) {
	s.mu.Lock()
	s.ranges = append(s.ranges, [2]int64{offset, length})
	s.mu.Unlock()
	if offset%podfs.RangeAlign != 0 {
		return nil, 0, errors.New("unaligned offset")
	}
	end := min(int64(len(s.data)), offset+length)
	if offset >= end {
		return io.NopCloser(bytes.NewReader(nil)), int64(len(s.data)), nil
	}
	return io.NopCloser(bytes.NewReader(s.data[offset:end])), int64(len(s.data)), nil
}

func (s *rangeExecSession) recorded() [][2]int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][2]int64(nil), s.ranges...)
}

// containerFileItem lists the session's root and returns its first file row.
func containerFileItem(t *testing.T, session podfs.ExecSession) *PodFSFileItem {
	t.Helper()
	deps := Deps{Ctx: t.Context(), PodFSFactory: fakePodFSFactory{session: session}}
	handle := newContainerSessionHandle(t.Context(), deps.PodFSFactory, "ns", "pod", "app")
	folder := NewPodContainerFSFolder(deps, []string{"containers", "pod", "root"}, "/", handle, false)
	rows := loadedLines(t, folder, 10)
	for _, r := range rows {
		if item, ok := r.(*PodFSFileItem); ok {
			return item
		}
	}
	t.Fatalf("no file row in %d rows", len(rows))
	return nil
}

func bigContent(n int) []byte {
	data := bytes.Repeat([]byte("0123456789abcdef\n"), n/17+1)
	return data[:n]
}

func TestContainerF3PagesALargeFile(t *testing.T) {
	data := bigContent(2 << 20)
	session := &rangeExecSession{data: data}
	session.entries = []podfs.FileEntry{{Name: "big.log", Type: podfs.EntryTypeFile, Size: int64(len(data)), UpdatedAt: time.Unix(1, 0)}}
	item := containerFileItem(t, session)

	view, err := item.OpenFileView(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if view.Source == nil || view.Body != "" || view.Size != int64(len(data)) || view.Title != "/big.log" {
		t.Fatalf("view = %+v, want a paged source of %d bytes", view, len(data))
	}
	if got := session.recorded(); len(got) != 0 {
		t.Fatalf("opening the view read %v", got)
	}

	// Read the first page and then the end, as the viewer does for Home
	// and End: only those blocks are asked for.
	doc := pagedfile.NewDocument(view.Size, pagedfile.Options{})
	load := func(missing []int64) {
		for _, b := range missing {
			buf := make([]byte, doc.BlockSize())
			n, err := view.Source.ReadAt(t.Context(), buf, b*doc.BlockSize())
			if err != nil && !errors.Is(err, io.EOF) {
				t.Fatal(err)
			}
			size, _ := view.Source.Size()
			doc.Put(b, buf[:n], size)
		}
	}
	_, _, missing := doc.Line(0)
	load(missing)
	if text, _, missing := doc.Line(0); missing != nil || string(text) != "0123456789abcdef" {
		t.Fatalf("first line = %q, missing %v", text, missing)
	}
	_, missing = doc.Prev(doc.Size())
	load(missing)
	if _, missing := doc.Prev(doc.Size()); missing != nil {
		t.Fatalf("the last line still misses %v", missing)
	}
	bs := int64(pagedfile.DefaultBlockSize)
	last := (int64(len(data)) - 1) / bs * bs
	want := [][2]int64{{0, bs}, {last, bs}}
	if got := session.recorded(); len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("ranges = %v, want %v", got, want)
	}

	// A read at an offset inside a dd block asks for the aligned range
	// and drops the bytes before off.
	buf := make([]byte, 100)
	n, err := view.Source.ReadAt(t.Context(), buf, 5000)
	if err != nil || n != 100 || !bytes.Equal(buf, data[5000:5100]) {
		t.Fatalf("ReadAt(5000) = %d, %v", n, err)
	}
	if got := session.recorded(); got[len(got)-1] != [2]int64{4096, 1004} {
		t.Fatalf("unaligned read asked for %v", got[len(got)-1])
	}
}

func TestContainerF3ReadsASmallFileWhole(t *testing.T) {
	session := &rangeExecSession{}
	session.entries = []podfs.FileEntry{{Name: "app.yaml", Type: podfs.EntryTypeFile, Size: 10, UpdatedAt: time.Unix(1, 0)}}
	session.fileData = map[string]string{"/app.yaml": "kind: Pod\n"}
	view, err := containerFileItem(t, session).OpenFileView(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if view.Source != nil || view.Body != "kind: Pod\n" {
		t.Fatalf("view = %+v, want the whole body", view)
	}
}

func TestContainerF3PagesAFileThatGrewSinceTheListing(t *testing.T) {
	data := bigContent(ContainerViewWholeBytes + 100)
	session := &rangeExecSession{data: data}
	session.entries = []podfs.FileEntry{{Name: "grows.log", Type: podfs.EntryTypeFile, Size: 10, UpdatedAt: time.Unix(1, 0)}}
	session.fileData = map[string]string{"/grows.log": string(data)}
	view, err := containerFileItem(t, session).OpenFileView(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if view.Source == nil {
		t.Fatalf("a file larger than the limit came whole, %d bytes", len(view.Body))
	}
}

func TestContainerF5StreamsTheWholeFile(t *testing.T) {
	data := bigContent(ContainerViewWholeBytes * 3)
	session := &rangeExecSession{data: data}
	session.entries = []podfs.FileEntry{{Name: "big.log", Type: podfs.EntryTypeFile, Size: int64(len(data)), UpdatedAt: time.Unix(1, 0)}}
	session.fileData = map[string]string{"/big.log": string(data)}
	item := containerFileItem(t, session)
	if item.ContentPath() != "/big.log" {
		t.Fatalf("ContentPath = %q", item.ContentPath())
	}
	rc, err := item.OpenContent(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rc)
	if cerr := rc.Close(); err != nil || cerr != nil {
		t.Fatalf("read: %v, close: %v", err, cerr)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("export has %d bytes, want all %d", len(got), len(data))
	}
}

func TestContainerPagedReadNeedsRangeReads(t *testing.T) {
	session := &fakeExecSession{entries: []podfs.FileEntry{{Name: "big.log", Type: podfs.EntryTypeFile, Size: 4 << 20, UpdatedAt: time.Unix(1, 0)}}}
	view, err := containerFileItem(t, session).OpenFileView(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := view.Source.ReadAt(t.Context(), make([]byte, 10), 0); err == nil {
		t.Fatal("a session without range reads served a block")
	}
}
