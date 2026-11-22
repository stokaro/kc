package filexfer_test

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stokaro/kc/internal/filexfer"
	"github.com/stokaro/kc/internal/podfs"
)

// fakeFS is a container filesystem for the walker: directories map to their
// entries, files to their content.
type fakeFS struct {
	dirs  map[string][]podfs.FileEntry
	files map[string][]byte
	// readErr fails ReadFile for these paths.
	readErr map[string]error
	// block makes ReadFile of this path wait for the context.
	block string
	// started is closed when the blocked read begins.
	started chan struct{}

	mu    sync.Mutex
	reads []string
}

func (f *fakeFS) List(_ context.Context, dir string) ([]podfs.FileEntry, error) {
	entries, ok := f.dirs[dir]
	if !ok {
		return nil, fmt.Errorf("ENOENT %s", dir)
	}
	return entries, nil
}

func (f *fakeFS) ReadFile(ctx context.Context, p string, _ int64) (io.ReadCloser, error) {
	f.mu.Lock()
	f.reads = append(f.reads, p)
	f.mu.Unlock()
	if err := f.readErr[p]; err != nil {
		return nil, err
	}
	if p == f.block {
		close(f.started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	data, ok := f.files[p]
	if !ok {
		return nil, fmt.Errorf("ENOENT %s", p)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

// tarFS serves a fixed archive as the tar stream and fails the walker, so
// a test knows which path ran.
type tarFS struct {
	archive []byte
	// tail is the error the stream returns after the archive (tar's exit status).
	tail error
	// openErr fails the stream before any byte.
	openErr error
	walked  bool
}

func (t *tarFS) TarStream(context.Context, string, string) (io.ReadCloser, error) {
	if t.openErr != nil {
		return io.NopCloser(&errReader{err: t.openErr}), nil
	}
	var r io.Reader = bytes.NewReader(t.archive)
	if t.tail != nil {
		r = io.MultiReader(r, &errReader{err: t.tail})
	}
	return io.NopCloser(r), nil
}

func (t *tarFS) List(context.Context, string) ([]podfs.FileEntry, error) {
	t.walked = true
	return nil, errors.New("walker must not run")
}

func (t *tarFS) ReadFile(context.Context, string, int64) (io.ReadCloser, error) {
	t.walked = true
	return nil, errors.New("walker must not run")
}

type errReader struct{ err error }

func (e *errReader) Read([]byte) (int, error) { return 0, e.err }

type tarEntry struct {
	name     string
	typeflag byte
	body     string
	link     string
	mode     int64
}

func buildTar(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		mode := e.mode
		if mode == 0 {
			mode = 0o644
		}
		hdr := &tar.Header{Name: e.name, Typeflag: e.typeflag, Mode: mode, Linkname: e.link, ModTime: time.Unix(1700000000, 0)}
		if e.typeflag == tar.TypeReg {
			hdr.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("tar header %s: %v", e.name, err)
		}
		if e.typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatalf("tar body %s: %v", e.name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	return buf.Bytes()
}

// layout is a sandbox: dest is the copy's destination, outside a sibling
// directory a safe copy never touches.
type layout struct {
	dest, outside string
}

func newLayout(t *testing.T) layout {
	t.Helper()
	base := t.TempDir()
	l := layout{dest: filepath.Join(base, "dest"), outside: filepath.Join(base, "outside")}
	for _, d := range []string{l.dest, l.outside} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return l
}

// tree lists every path under dir with its type, "d" or "f" or "l".
func tree(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == dir {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		kind := "f"
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			kind = "l"
		case d.IsDir():
			kind = "d"
		}
		out = append(out, kind+" "+filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

func dirEntry(name string) podfs.FileEntry {
	return podfs.FileEntry{Name: name, Type: podfs.EntryTypeDir, Mode: 0o40755}
}

func fileEntry(name string, mode uint32) podfs.FileEntry {
	return podfs.FileEntry{Name: name, Type: podfs.EntryTypeFile, Mode: 0o100000 | mode, UpdatedAt: time.Unix(1700000000, 0)}
}

func TestTarExtractionRefusesEscapes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		entries []tarEntry
		// prepare sets up the destination before the copy.
		prepare func(t *testing.T, l layout)
		policy  filexfer.Policy
		status  filexfer.Status
		errIs   error
		// want is the destination tree after the copy.
		want []string
	}{
		{
			name:    "parent traversal",
			entries: []tarEntry{{name: "./logs/", typeflag: tar.TypeDir}, {name: "../evil", typeflag: tar.TypeReg, body: "x"}},
			status:  filexfer.StatusFailed, errIs: filexfer.ErrUnsafePath,
		},
		{
			name:    "traversal inside the entry",
			entries: []tarEntry{{name: "./logs/", typeflag: tar.TypeDir}, {name: "./logs/../../evil", typeflag: tar.TypeReg, body: "x"}},
			status:  filexfer.StatusFailed, errIs: filexfer.ErrUnsafePath,
		},
		{
			name:    "dot-dot that stays inside",
			entries: []tarEntry{{name: "./logs/", typeflag: tar.TypeDir}, {name: "./logs/a/../b", typeflag: tar.TypeReg, body: "x"}},
			status:  filexfer.StatusFailed, errIs: filexfer.ErrUnsafePath,
		},
		{
			name:    "absolute name",
			entries: []tarEntry{{name: "./logs/", typeflag: tar.TypeDir}, {name: "/etc/passwd", typeflag: tar.TypeReg, body: "x"}},
			status:  filexfer.StatusFailed, errIs: filexfer.ErrUnsafePath,
		},
		{
			name:    "entry outside the copied name",
			entries: []tarEntry{{name: "./logs/", typeflag: tar.TypeDir}, {name: "./other/file", typeflag: tar.TypeReg, body: "x"}},
			status:  filexfer.StatusFailed, errIs: filexfer.ErrUnsafePath,
		},
		{
			name:    "hard link to an absolute path",
			entries: []tarEntry{{name: "./logs/", typeflag: tar.TypeDir}, {name: "./logs/h", typeflag: tar.TypeLink, link: "/etc/passwd"}},
			status:  filexfer.StatusFailed, errIs: filexfer.ErrUnsafePath,
		},
		{
			name: "symlink then a file through it",
			entries: []tarEntry{
				{name: "./logs/", typeflag: tar.TypeDir},
				{name: "./logs/link", typeflag: tar.TypeSymlink, link: "../../outside"},
				{name: "./logs/link/passwd", typeflag: tar.TypeReg, body: "x"},
			},
			// The link is not created, so the file lands in a real
			// directory inside the copy.
			status: filexfer.StatusCopied,
			want:   []string{"d logs", "d logs/link", "f logs/link/passwd"},
		},
		{
			name:    "local symlink at the destination name",
			entries: []tarEntry{{name: "./logs/", typeflag: tar.TypeDir}, {name: "./logs/a", typeflag: tar.TypeReg, body: "x"}},
			prepare: func(t *testing.T, l layout) {
				if err := os.Symlink(l.outside, filepath.Join(l.dest, "logs")); err != nil {
					t.Fatal(err)
				}
			},
			policy: filexfer.Overwrite,
			status: filexfer.StatusFailed, errIs: filexfer.ErrSymlink,
			want: []string{"l logs"},
		},
		{
			name:    "local symlink inside a merged directory",
			entries: []tarEntry{{name: "./logs/", typeflag: tar.TypeDir}, {name: "./logs/sub/a", typeflag: tar.TypeReg, body: "x"}},
			prepare: func(t *testing.T, l layout) {
				if err := os.Mkdir(filepath.Join(l.dest, "logs"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(l.outside, filepath.Join(l.dest, "logs", "sub")); err != nil {
					t.Fatal(err)
				}
			},
			policy: filexfer.Overwrite,
			status: filexfer.StatusFailed, errIs: filexfer.ErrSymlink,
			want: []string{"d logs", "l logs/sub"},
		},
		{
			name: "hard link inside the entry",
			entries: []tarEntry{
				{name: "./logs/", typeflag: tar.TypeDir},
				{name: "./logs/a", typeflag: tar.TypeReg, body: "same"},
				{name: "./logs/b", typeflag: tar.TypeLink, link: "./logs/a"},
			},
			status: filexfer.StatusCopied,
			want:   []string{"d logs", "f logs/a", "f logs/b"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l := newLayout(t)
			if tc.prepare != nil {
				tc.prepare(t, l)
			}
			src := &tarFS{archive: buildTar(t, tc.entries...)}
			results, err := filexfer.Download(t.Context(), src, filexfer.Request{
				Dir: "/var", Entries: []podfs.FileEntry{dirEntry("logs")}, Dest: l.dest, Policy: tc.policy,
			}, nil)
			if err != nil {
				t.Fatalf("Download: %v", err)
			}
			if len(results) != 1 {
				t.Fatalf("results = %d, want 1", len(results))
			}
			res := results[0]
			if res.Status != tc.status {
				t.Fatalf("status = %s (%s), want %s", res.Status, res.Message, tc.status)
			}
			if tc.errIs != nil && !errors.Is(res.Err, tc.errIs) {
				t.Fatalf("err = %v, want %v", res.Err, tc.errIs)
			}
			if src.walked {
				t.Fatalf("an archive with entries must not fall back to the walker")
			}
			if got := tree(t, l.outside); len(got) != 0 {
				t.Fatalf("outside the destination: %v", got)
			}
			if got := tree(t, l.dest); strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("destination = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDownloadRejectsBadEntryNames(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"..", ".", "a/b", "", `a\b`} {
		t.Run(fmt.Sprintf("%q", name), func(t *testing.T) {
			t.Parallel()
			l := newLayout(t)
			results, err := filexfer.Download(t.Context(), &fakeFS{}, filexfer.Request{
				Dir: "/", Entries: []podfs.FileEntry{fileEntry(name, 0o644)}, Dest: l.dest,
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if results[0].Status != filexfer.StatusFailed || !errors.Is(results[0].Err, filexfer.ErrUnsafePath) {
				t.Fatalf("result = %+v, want unsafe path failure", results[0])
			}
		})
	}
}

// binary holds every byte value, so a text-mode copy would show.
func binary() []byte {
	b := make([]byte, 0, 512)
	for i := range 512 {
		b = append(b, byte(i))
	}
	return b
}

func nestedFS() *fakeFS {
	return &fakeFS{
		dirs: map[string][]podfs.FileEntry{
			"/data/app":     {dirEntry("bin"), fileEntry("config.yaml", 0o644), {Name: "current", Type: podfs.EntryTypeSymlink, Target: "/etc"}},
			"/data/app/bin": {fileEntry("run", 0o4755), {Name: "fifo", Type: podfs.EntryTypePipe}},
		},
		files: map[string][]byte{
			"/data/app/config.yaml": []byte("key: value\n"),
			"/data/app/bin/run":     binary(),
		},
	}
}

func TestDownloadNestedDirectoryThroughWalker(t *testing.T) {
	t.Parallel()
	l := newLayout(t)
	counter := &filexfer.Counter{}
	results, err := filexfer.Download(t.Context(), nestedFS(), filexfer.Request{
		Dir: "/data", Entries: []podfs.FileEntry{dirEntry("app")}, Dest: l.dest,
	}, counter)
	if err != nil {
		t.Fatal(err)
	}
	res := results[0]
	if res.Status != filexfer.StatusCopied {
		t.Fatalf("status = %s: %s", res.Status, res.Message)
	}
	want := []string{"d app", "d app/bin", "f app/bin/run", "f app/config.yaml"}
	if got := tree(t, l.dest); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("tree = %v, want %v", got, want)
	}
	got, err := os.ReadFile(filepath.Join(l.dest, "app", "bin", "run"))
	if err != nil || !bytes.Equal(got, binary()) {
		t.Fatalf("binary content changed (err %v)", err)
	}
	for p, want := range map[string]fs.FileMode{
		"app":             filexfer.DirMode,
		"app/bin":         filexfer.DirMode,
		"app/bin/run":     0o700, // 4755: owner bits only, no setuid
		"app/config.yaml": 0o600,
	} {
		info, err := os.Stat(filepath.Join(l.dest, p))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != want || info.Mode()&(fs.ModeSetuid|fs.ModeSetgid) != 0 {
			t.Errorf("%s mode = %v, want %v", p, info.Mode(), want)
		}
	}
	wantNotes := []string{"app/bin/fifo: special file not copied", "app/current: symbolic link not copied"}
	if strings.Join(res.Notes, ",") != strings.Join(wantNotes, ",") {
		t.Fatalf("notes = %v, want %v", res.Notes, wantNotes)
	}
	wantBytes := int64(len(binary()) + len("key: value\n"))
	if res.Files != 2 || res.Bytes != wantBytes {
		t.Fatalf("result counts = %d files, %d bytes; want 2, %d", res.Files, res.Bytes, wantBytes)
	}
	if p := counter.Snapshot(); p.Files != 2 || p.Bytes != wantBytes || p.Index != 1 || p.Total != 1 || p.Current != "app" {
		t.Fatalf("progress = %+v", p)
	}
}

func TestDownloadUsesTarAndFallsBackWithoutIt(t *testing.T) {
	t.Parallel()
	archive := func(t *testing.T) []byte {
		return buildTar(t,
			tarEntry{name: "./app/", typeflag: tar.TypeDir},
			tarEntry{name: "./app/config.yaml", typeflag: tar.TypeReg, body: "from tar"},
		)
	}
	t.Run("tar stream", func(t *testing.T) {
		t.Parallel()
		l := newLayout(t)
		src := &tarFS{archive: archive(t)}
		results, _ := filexfer.Download(t.Context(), src, filexfer.Request{Dir: "/data", Entries: []podfs.FileEntry{dirEntry("app")}, Dest: l.dest}, nil)
		if results[0].Status != filexfer.StatusCopied || src.walked {
			t.Fatalf("result = %+v, walked = %v", results[0], src.walked)
		}
		if got, _ := os.ReadFile(filepath.Join(l.dest, "app", "config.yaml")); string(got) != "from tar" {
			t.Fatalf("content = %q", got)
		}
	})
	t.Run("tar fails at the end", func(t *testing.T) {
		t.Parallel()
		l := newLayout(t)
		src := &tarFS{archive: archive(t), tail: errors.New("tar: ./app/secret: Permission denied")}
		results, _ := filexfer.Download(t.Context(), src, filexfer.Request{Dir: "/data", Entries: []podfs.FileEntry{dirEntry("app")}, Dest: l.dest}, nil)
		if results[0].Status != filexfer.StatusFailed || !strings.Contains(results[0].Message, "Permission denied") {
			t.Fatalf("result = %+v", results[0])
		}
		if got := tree(t, l.dest); len(got) != 0 {
			t.Fatalf("a failed directory must leave nothing: %v", got)
		}
	})
	t.Run("no tar in the image", func(t *testing.T) {
		t.Parallel()
		l := newLayout(t)
		src := &walkWithTar{fakeFS: nestedFS(), err: errors.New(`exec: "tar": executable file not found in $PATH`)}
		results, _ := filexfer.Download(t.Context(), src, filexfer.Request{Dir: "/data", Entries: []podfs.FileEntry{dirEntry("app")}, Dest: l.dest}, nil)
		if results[0].Status != filexfer.StatusCopied {
			t.Fatalf("result = %+v", results[0])
		}
		if !src.tried || len(src.reads) == 0 {
			t.Fatalf("tar tried = %v, walker reads = %v", src.tried, src.reads)
		}
	})
}

// walkWithTar has a tar stream that fails before its first byte.
type walkWithTar struct {
	*fakeFS
	err   error
	tried bool
}

func (w *walkWithTar) TarStream(context.Context, string, string) (io.ReadCloser, error) {
	w.tried = true
	return io.NopCloser(&errReader{err: w.err}), nil
}

func TestDownloadCancellation(t *testing.T) {
	t.Parallel()
	l := newLayout(t)
	src := nestedFS()
	src.dirs["/data"] = []podfs.FileEntry{fileEntry("first.txt", 0o644), dirEntry("app"), fileEntry("last.txt", 0o644)}
	src.files["/data/first.txt"] = []byte("one")
	src.files["/data/last.txt"] = []byte("three")
	src.block = "/data/app/config.yaml"
	src.started = make(chan struct{})

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		<-src.started
		cancel()
	}()
	results, err := filexfer.Download(ctx, src, filexfer.Request{
		Dir: "/data", Entries: src.dirs["/data"], Dest: l.dest,
	}, &filexfer.Counter{})
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(results))
	for _, r := range results {
		got = append(got, r.Name+"="+string(r.Status))
	}
	want := []string{"first.txt=copied", "app=canceled", "last.txt=canceled"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("results = %v, want %v", got, want)
	}
	if results[2].Message != "not started" {
		t.Fatalf("last message = %q", results[2].Message)
	}
	// Only the entry finished before the cancel stays: no staging
	// directory, no temporary file, no half of app.
	if tree := tree(t, l.dest); strings.Join(tree, ",") != "f first.txt" {
		t.Fatalf("destination = %v", tree)
	}
}

func TestDownloadPartialFailure(t *testing.T) {
	t.Parallel()
	l := newLayout(t)
	src := nestedFS()
	src.dirs["/data"] = []podfs.FileEntry{fileEntry("ok.txt", 0o644), dirEntry("app")}
	src.files["/data/ok.txt"] = []byte("fine")
	src.readErr = map[string]error{"/data/app/config.yaml": errors.New("cat: EACCES /data/app/config.yaml")}
	results, err := filexfer.Download(t.Context(), src, filexfer.Request{Dir: "/data", Entries: src.dirs["/data"], Dest: l.dest}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Status != filexfer.StatusCopied {
		t.Fatalf("ok.txt = %+v", results[0])
	}
	if results[1].Status != filexfer.StatusFailed || !strings.Contains(results[1].Message, "EACCES") {
		t.Fatalf("app = %+v", results[1])
	}
	if strings.Contains(results[1].Message, "kc-partial") {
		t.Fatalf("message names the staging directory: %q", results[1].Message)
	}
	if tree := tree(t, l.dest); strings.Join(tree, ",") != "f ok.txt" {
		t.Fatalf("destination = %v", tree)
	}
}

func TestDownloadOverwriteHandling(t *testing.T) {
	t.Parallel()
	src := func() *fakeFS {
		return &fakeFS{
			dirs: map[string][]podfs.FileEntry{"/data/app": {fileEntry("config.yaml", 0o644)}},
			files: map[string][]byte{
				"/data/notes.txt":       []byte("new"),
				"/data/app/config.yaml": []byte("new config"),
			},
		}
	}
	cases := []struct {
		name    string
		policy  filexfer.Policy
		entry   podfs.FileEntry
		prepare func(t *testing.T, dest string)
		status  filexfer.Status
		errIs   error
		check   func(t *testing.T, dest string)
	}{
		{
			name: "skip keeps an existing file", policy: filexfer.SkipExisting, entry: fileEntry("notes.txt", 0o644),
			prepare: writeLocal("notes.txt", "old", 0o644), status: filexfer.StatusSkipped,
			check: wantContent("notes.txt", "old"),
		},
		{
			name: "overwrite replaces a file", policy: filexfer.Overwrite, entry: fileEntry("notes.txt", 0o644),
			prepare: writeLocal("notes.txt", "old", 0o644), status: filexfer.StatusCopied,
			check: wantContent("notes.txt", "new"),
		},
		{
			name: "overwrite refuses a read-only file", policy: filexfer.Overwrite, entry: fileEntry("notes.txt", 0o644),
			prepare: writeLocal("notes.txt", "old", 0o444), status: filexfer.StatusFailed, errIs: filexfer.ErrReadOnly,
			check: wantContent("notes.txt", "old"),
		},
		{
			name: "overwrite refuses a file over a directory", policy: filexfer.Overwrite, entry: fileEntry("notes.txt", 0o644),
			prepare: func(t *testing.T, dest string) { mustMkdir(t, filepath.Join(dest, "notes.txt")) },
			status:  filexfer.StatusFailed, errIs: filexfer.ErrNotRegular,
		},
		{
			name: "skip keeps an existing directory", policy: filexfer.SkipExisting, entry: dirEntry("app"),
			prepare: writeLocal("app/config.yaml", "old config", 0o644), status: filexfer.StatusSkipped,
			check: wantContent("app/config.yaml", "old config"),
		},
		{
			name: "overwrite merges a directory", policy: filexfer.Overwrite, entry: dirEntry("app"),
			prepare: func(t *testing.T, dest string) {
				writeLocal("app/config.yaml", "old config", 0o644)(t, dest)
				writeLocal("app/local-only.txt", "mine", 0o644)(t, dest)
			},
			status: filexfer.StatusCopied,
			check: func(t *testing.T, dest string) {
				wantContent("app/config.yaml", "new config")(t, dest)
				wantContent("app/local-only.txt", "mine")(t, dest)
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l := newLayout(t)
			tc.prepare(t, l.dest)
			results, err := filexfer.Download(t.Context(), src(), filexfer.Request{
				Dir: "/data", Entries: []podfs.FileEntry{tc.entry}, Dest: l.dest, Policy: tc.policy,
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if results[0].Status != tc.status {
				t.Fatalf("status = %s (%s), want %s", results[0].Status, results[0].Message, tc.status)
			}
			if tc.errIs != nil && !errors.Is(results[0].Err, tc.errIs) {
				t.Fatalf("err = %v, want %v", results[0].Err, tc.errIs)
			}
			if tc.check != nil {
				tc.check(t, l.dest)
			}
			for _, p := range tree(t, l.dest) {
				if strings.Contains(p, ".kc-") {
					t.Fatalf("leftover temporary path %s", p)
				}
			}
		})
	}
}

func TestDownloadSkipsTopLevelSymlinks(t *testing.T) {
	t.Parallel()
	l := newLayout(t)
	results, err := filexfer.Download(t.Context(), &fakeFS{}, filexfer.Request{
		Dir: "/", Entries: []podfs.FileEntry{{Name: "etc-link", Type: podfs.EntryTypeSymlink, Target: "/etc"}}, Dest: l.dest,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Status != filexfer.StatusSkipped {
		t.Fatalf("result = %+v", results[0])
	}
	if got := tree(t, l.dest); len(got) != 0 {
		t.Fatalf("destination = %v", got)
	}
}

func writeLocal(name, content string, mode fs.FileMode) func(*testing.T, string) {
	return func(t *testing.T, dest string) {
		t.Helper()
		p := filepath.Join(dest, filepath.FromSlash(name))
		mustMkdir(t, filepath.Dir(p))
		if err := os.WriteFile(p, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
}

func wantContent(name, content string) func(*testing.T, string) {
	return func(t *testing.T, dest string) {
		t.Helper()
		got, err := os.ReadFile(filepath.Join(dest, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != content {
			t.Fatalf("%s = %q, want %q", name, got, content)
		}
	}
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}
