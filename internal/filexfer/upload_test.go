package filexfer_test

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/stokaro/kc/internal/filexfer"
	"github.com/stokaro/kc/internal/podfs"
)

// member is one entry of an uploaded archive, as the container saw it.
type member struct {
	typ  byte
	mode int64
	uid  int
	data string
}

// fakeContainer is a container directory that receives archives. It reads
// each stream to the end, like tar -xf -, and "moves the entry into place"
// only when the completion marker arrived, like the extract script.
type fakeContainer struct {
	listing []podfs.FileEntry
	listErr error
	// fail makes ExtractTar fail with this error after reading the stream.
	fail error
	// onRead runs after the first member was read, for cancellation tests.
	onRead func()

	mu       sync.Mutex
	requests []podfs.ExtractRequest
	// placed holds the members of every entry moved into place, by name.
	placed map[string]map[string]member
	// cut counts streams that ended without the marker.
	cut int
}

func (c *fakeContainer) List(_ context.Context, dir string) ([]podfs.FileEntry, error) {
	if c.listErr != nil {
		return nil, c.listErr
	}
	return c.listing, nil
}

func (c *fakeContainer) ExtractTar(ctx context.Context, req podfs.ExtractRequest, archive io.Reader) error {
	c.mu.Lock()
	c.requests = append(c.requests, req)
	c.mu.Unlock()
	tr := tar.NewReader(archive)
	got := make(map[string]member)
	complete := false
	first := true
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			c.mu.Lock()
			c.cut++
			c.mu.Unlock()
			return fmt.Errorf("tar: %w", err)
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			c.mu.Lock()
			c.cut++
			c.mu.Unlock()
			return fmt.Errorf("tar: %w", err)
		}
		if hdr.Name == podfs.UploadCompleteMarker {
			complete = true
			continue
		}
		got[hdr.Name] = member{typ: hdr.Typeflag, mode: hdr.Mode, uid: hdr.Uid, data: string(data)}
		if first && c.onRead != nil {
			first = false
			c.onRead()
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.fail != nil {
		return c.fail
	}
	if !complete {
		c.mu.Lock()
		c.cut++
		c.mu.Unlock()
		return errors.New("the archive is incomplete")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.placed == nil {
		c.placed = make(map[string]map[string]member)
	}
	c.placed[req.Name] = got
	return nil
}

// localTree creates files (content) and directories (trailing slash) under
// a new temporary directory.
func localTree(t *testing.T, entries map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range entries {
		p := filepath.Join(dir, filepath.FromSlash(strings.TrimSuffix(name, "/")))
		if strings.HasSuffix(name, "/") {
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestUploadBuildsSafeArchives(t *testing.T) {
	t.Parallel()
	src := localTree(t, map[string]string{
		"app/":               "",
		"app/config.yaml":    "a: 1",
		"app/bin/run.sh":     "#!/bin/sh",
		"app/empty/":         "",
		"notes.txt":          "hello",
		"outside/secret.txt": "secret",
	})
	if err := os.Chmod(filepath.Join(src, "app/bin/run.sh"), 0o4755); err != nil {
		t.Fatal(err)
	}
	// A link inside the tree points out of it; its target must not travel.
	if err := os.Symlink(filepath.Join(src, "outside"), filepath.Join(src, "app/leak")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(src, "app/fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := &fakeContainer{}
	counter := &filexfer.Counter{}
	results, err := filexfer.Upload(t.Context(), c, filexfer.UploadRequest{
		Src: src, Names: []string{"app", "notes.txt"}, Dir: "/srv",
	}, counter)
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if len(results) != 2 || results[0].Status != filexfer.StatusCopied || results[1].Status != filexfer.StatusCopied {
		t.Fatalf("results = %+v", results)
	}
	app := c.placed["app"]
	want := map[string]member{
		"app/":            {typ: tar.TypeDir, mode: 0o755},
		"app/bin/":        {typ: tar.TypeDir, mode: 0o755},
		"app/bin/run.sh":  {typ: tar.TypeReg, mode: 0o755, data: "#!/bin/sh"},
		"app/config.yaml": {typ: tar.TypeReg, mode: 0o644, data: "a: 1"},
		"app/empty/":      {typ: tar.TypeDir, mode: 0o755},
	}
	if len(app) != len(want) {
		t.Fatalf("app members = %+v, want %+v", app, want)
	}
	for name, w := range want {
		if got := app[name]; got != w {
			t.Errorf("member %s = %+v, want %+v", name, got, w)
		}
	}
	for name := range app {
		if strings.Contains(name, "..") || strings.HasPrefix(name, "/") || strings.Contains(name, "secret") {
			t.Errorf("unsafe member %q", name)
		}
	}
	if got := results[0].Notes; len(got) != 2 || !strings.Contains(strings.Join(got, "\n"), "app/leak: symbolic link") || !strings.Contains(strings.Join(got, "\n"), "app/fifo: special file") {
		t.Errorf("notes = %q", got)
	}
	if results[0].Files != 2 || results[0].Bytes != int64(len("a: 1")+len("#!/bin/sh")) {
		t.Errorf("app counts = %d files, %d bytes", results[0].Files, results[0].Bytes)
	}
	if got := c.placed["notes.txt"]["notes.txt"]; got.data != "hello" || got.typ != tar.TypeReg {
		t.Errorf("notes.txt = %+v", got)
	}
	for _, r := range c.requests {
		if r.Dir != "/srv" || r.Replace {
			t.Errorf("request %+v", r)
		}
	}
}

func TestUploadExistingEntries(t *testing.T) {
	t.Parallel()
	listing := []podfs.FileEntry{
		{Name: "file.txt", Type: podfs.EntryTypeFile},
		{Name: "dir", Type: podfs.EntryTypeDir},
		{Name: "link", Type: podfs.EntryTypeSymlink},
		{Name: "pipe", Type: podfs.EntryTypePipe},
		{Name: "isfile", Type: podfs.EntryTypeFile},
	}
	src := localTree(t, map[string]string{
		"file.txt": "new", "dir/a": "a", "link": "x", "pipe": "x", "isfile/b": "b", "fresh.txt": "f",
	})
	names := []string{"file.txt", "dir", "link", "pipe", "isfile", "fresh.txt"}

	t.Run("skip existing", func(t *testing.T) {
		t.Parallel()
		c := &fakeContainer{listing: listing}
		results, err := filexfer.Upload(t.Context(), c, filexfer.UploadRequest{Src: src, Names: names, Dir: "/d", Policy: filexfer.SkipExisting}, nil)
		if err != nil {
			t.Fatal(err)
		}
		for i, r := range results[:5] {
			if r.Status != filexfer.StatusSkipped || r.Message != "exists" {
				t.Errorf("results[%d] = %+v, want skipped", i, r)
			}
		}
		if results[5].Status != filexfer.StatusCopied {
			t.Errorf("fresh.txt = %+v", results[5])
		}
		if len(c.requests) != 1 || c.requests[0].Name != "fresh.txt" {
			t.Errorf("requests = %+v, want only fresh.txt", c.requests)
		}
	})

	t.Run("overwrite", func(t *testing.T) {
		t.Parallel()
		c := &fakeContainer{listing: listing}
		results, err := filexfer.Upload(t.Context(), c, filexfer.UploadRequest{Src: src, Names: names, Dir: "/d", Policy: filexfer.Overwrite}, nil)
		if err != nil {
			t.Fatal(err)
		}
		wantStatus := []filexfer.Status{
			filexfer.StatusCopied, filexfer.StatusCopied, filexfer.StatusFailed,
			filexfer.StatusFailed, filexfer.StatusFailed, filexfer.StatusCopied,
		}
		for i, r := range results {
			if r.Status != wantStatus[i] {
				t.Errorf("%s = %+v, want %s", r.Name, r, wantStatus[i])
			}
		}
		if !errors.Is(results[2].Err, filexfer.ErrSymlink) {
			t.Errorf("link error = %v, want ErrSymlink", results[2].Err)
		}
		if !errors.Is(results[3].Err, filexfer.ErrNotRegular) {
			t.Errorf("pipe error = %v, want ErrNotRegular", results[3].Err)
		}
		if !strings.Contains(results[4].Message, "not a directory") {
			t.Errorf("isfile message = %q", results[4].Message)
		}
		replace := make(map[string]bool)
		for _, r := range c.requests {
			replace[r.Name] = r.Replace
		}
		if want := map[string]bool{"file.txt": true, "dir": true, "fresh.txt": false}; fmt.Sprint(replace) != fmt.Sprint(want) {
			t.Errorf("requests = %v, want %v", replace, want)
		}
	})
}

func TestUploadRefusesNamesAndLinks(t *testing.T) {
	t.Parallel()
	src := localTree(t, map[string]string{"real/": ""})
	if err := os.Symlink("/etc", filepath.Join(src, "etc")); err != nil {
		t.Fatal(err)
	}
	c := &fakeContainer{}
	results, err := filexfer.Upload(t.Context(), c, filexfer.UploadRequest{
		Src: src, Names: []string{"../x", "/etc/passwd", "a/b", "..", "etc", "missing"}, Dir: "/d",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results[:4] {
		if r.Status != filexfer.StatusFailed || !errors.Is(r.Err, filexfer.ErrUnsafePath) {
			t.Errorf("%q = %+v, want unsafe path", r.Name, r)
		}
	}
	if r := results[4]; r.Status != filexfer.StatusSkipped || !strings.Contains(r.Message, "symbolic") {
		t.Errorf("link = %+v, want skipped", r)
	}
	if r := results[5]; r.Status != filexfer.StatusFailed || !errors.Is(r.Err, fs.ErrNotExist) {
		t.Errorf("missing = %+v, want not exist", r)
	}
	if len(c.requests) != 0 {
		t.Errorf("requests = %+v, want none", c.requests)
	}
}

func TestUploadCancellation(t *testing.T) {
	t.Parallel()
	src := localTree(t, map[string]string{"big/a": "a", "big/b": strings.Repeat("b", 1<<20), "later.txt": "x"})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	c := &fakeContainer{onRead: cancel}
	results, err := filexfer.Upload(ctx, c, filexfer.UploadRequest{Src: src, Names: []string{"big", "later.txt"}, Dir: "/d"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Status != filexfer.StatusCanceled || results[1].Status != filexfer.StatusCanceled || results[1].Message != "not started" {
		t.Fatalf("results = %+v", results)
	}
	if len(c.placed) != 0 {
		t.Errorf("placed = %v, want nothing", c.placed)
	}
	if len(c.requests) != 1 {
		t.Errorf("requests = %d, want 1", len(c.requests))
	}
}

func TestUploadWithoutTarStopsAfterFirstEntry(t *testing.T) {
	t.Parallel()
	src := localTree(t, map[string]string{"a": "a", "b": "b", "c": "c"})
	c := &fakeContainer{fail: podfs.ErrTarMissing}
	results, err := filexfer.Upload(t.Context(), c, filexfer.UploadRequest{Src: src, Names: []string{"a", "b", "c"}, Dir: "/d"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if r.Status != filexfer.StatusFailed || !errors.Is(r.Err, podfs.ErrTarMissing) {
			t.Errorf("%s = %+v, want tar missing", r.Name, r)
		}
	}
	if len(c.requests) != 1 {
		t.Errorf("requests = %d, want 1", len(c.requests))
	}
}

func TestUploadListingFailure(t *testing.T) {
	t.Parallel()
	src := localTree(t, map[string]string{"a": "a"})
	c := &fakeContainer{listErr: errors.New("ENOENT /d")}
	if _, err := filexfer.Upload(t.Context(), c, filexfer.UploadRequest{Src: src, Names: []string{"a"}, Dir: "/d"}, nil); err == nil || !strings.Contains(err.Error(), "ENOENT") {
		t.Fatalf("Upload = %v, want listing error", err)
	}
	if _, err := filexfer.Upload(t.Context(), c, filexfer.UploadRequest{Src: filepath.Join(src, "nope"), Names: []string{"a"}, Dir: "/d"}, nil); err == nil {
		t.Fatal("Upload from a missing directory succeeded")
	}
}
