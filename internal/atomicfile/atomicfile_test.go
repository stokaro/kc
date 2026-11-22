package atomicfile_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stokaro/kc/internal/atomicfile"
)

var errBroken = errors.New("stream broken")

// failingReader returns its data and then fails instead of returning io.EOF.
type failingReader struct {
	data []byte
	err  error
}

func (f *failingReader) Read(p []byte) (int, error) {
	if len(f.data) == 0 {
		return 0, f.err
	}
	n := copy(p, f.data)
	f.data = f.data[n:]
	return n, nil
}

// cancelingReader cancels the copy after it has returned its first chunk.
type cancelingReader struct {
	chunk  []byte
	cancel context.CancelFunc
	done   bool
}

func (c *cancelingReader) Read(p []byte) (int, error) {
	if c.done {
		return copy(p, c.chunk), nil
	}
	c.done = true
	c.cancel()
	return copy(p, c.chunk), nil
}

func binaryPayload() []byte {
	// Several copy buffers long, with every byte value including NUL and CR.
	var b bytes.Buffer
	for i := range 100_000 {
		b.WriteByte(byte(i * 7))
	}
	return b.Bytes()
}

type existing struct {
	data []byte
	mode fs.FileMode
}

func TestWrite(t *testing.T) {
	t.Parallel()

	old := []byte("previous content\n")
	payload := binaryPayload()

	cases := []struct {
		name     string
		existing *existing
		reader   func(cancel context.CancelFunc) io.Reader
		canceled bool
		wantErr  error
		wantData []byte
		wantMode fs.FileMode
	}{
		{
			name:     "new file is private",
			reader:   func(context.CancelFunc) io.Reader { return strings.NewReader("hello") },
			wantData: []byte("hello"),
			wantMode: 0o600,
		},
		{
			name:     "new file keeps exact binary contents",
			reader:   func(context.CancelFunc) io.Reader { return bytes.NewReader(payload) },
			wantData: payload,
			wantMode: 0o600,
		},
		{
			name:     "empty content creates an empty file",
			reader:   func(context.CancelFunc) io.Reader { return strings.NewReader("") },
			wantData: make([]byte, 0),
			wantMode: 0o600,
		},
		{
			name:     "replacing a world-readable file narrows it",
			existing: &existing{data: old, mode: 0o644},
			reader:   func(context.CancelFunc) io.Reader { return bytes.NewReader(payload) },
			wantData: payload,
			wantMode: 0o600,
		},
		{
			name:     "replacing a private file keeps it private",
			existing: &existing{data: old, mode: 0o600},
			reader:   func(context.CancelFunc) io.Reader { return strings.NewReader("new") },
			wantData: []byte("new"),
			wantMode: 0o600,
		},
		{
			name:     "replacing an executable file drops execute and group bits",
			existing: &existing{data: old, mode: 0o775},
			reader:   func(context.CancelFunc) io.Reader { return strings.NewReader("new") },
			wantData: []byte("new"),
			wantMode: 0o600,
		},
		{
			name:     "replacing a write-only file does not add read",
			existing: &existing{data: old, mode: 0o200},
			reader:   func(context.CancelFunc) io.Reader { return strings.NewReader("new") },
			wantData: []byte("new"),
			wantMode: 0o200,
		},
		{
			name:     "failing reader keeps the previous file",
			existing: &existing{data: old, mode: 0o644},
			reader: func(context.CancelFunc) io.Reader {
				return &failingReader{data: payload[:5000], err: errBroken}
			},
			wantErr:  errBroken,
			wantData: old,
			wantMode: 0o644,
		},
		{
			name: "failing reader creates no file",
			reader: func(context.CancelFunc) io.Reader {
				return &failingReader{data: payload[:5000], err: errBroken}
			},
			wantErr: errBroken,
		},
		{
			name:     "canceled before start keeps the previous file",
			existing: &existing{data: old, mode: 0o600},
			reader:   func(context.CancelFunc) io.Reader { return bytes.NewReader(payload) },
			canceled: true,
			wantErr:  context.Canceled,
			wantData: old,
			wantMode: 0o600,
		},
		{
			name:     "canceled during the copy keeps the previous file",
			existing: &existing{data: old, mode: 0o640},
			reader: func(cancel context.CancelFunc) io.Reader {
				return &cancelingReader{chunk: payload[:1024], cancel: cancel}
			},
			wantErr:  context.Canceled,
			wantData: old,
			wantMode: 0o640,
		},
		{
			name: "canceled during the copy creates no file",
			reader: func(cancel context.CancelFunc) io.Reader {
				return &cancelingReader{chunk: payload[:1024], cancel: cancel}
			},
			wantErr: context.Canceled,
		},
		{
			name:     "read-only file is refused",
			existing: &existing{data: old, mode: 0o444},
			reader:   func(context.CancelFunc) io.Reader { return strings.NewReader("new") },
			wantErr:  atomicfile.ErrReadOnly,
			wantData: old,
			wantMode: 0o444,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			dest := filepath.Join(dir, "export.yaml")
			if tc.existing != nil {
				writeFile(t, dest, tc.existing.data, tc.existing.mode)
			}
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			if tc.canceled {
				cancel()
			}

			err := atomicfile.Write(ctx, dest, tc.reader(cancel))

			if tc.wantErr == nil && err != nil {
				t.Fatalf("Write() error = %v, want nil", err)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("Write() error = %v, want %v", err, tc.wantErr)
			}
			if tc.wantData == nil {
				assertDirEntries(t, dir)
				return
			}
			assertDirEntries(t, dir, "export.yaml")
			assertFile(t, dest, tc.wantData, tc.wantMode)
		})
	}
}

func TestWriteRefusesSymlink(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim")
	link := filepath.Join(dir, "link")
	writeFile(t, victim, []byte("keep me"), 0o644)
	if err := os.Symlink(victim, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	err := atomicfile.Write(t.Context(), link, strings.NewReader("secret"))
	if !errors.Is(err, atomicfile.ErrSymlink) {
		t.Fatalf("Write() error = %v, want %v", err, atomicfile.ErrSymlink)
	}
	assertFile(t, victim, []byte("keep me"), 0o644)
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("lstat link: %v", err)
	}
	if info.Mode()&fs.ModeSymlink == 0 {
		t.Fatalf("link was replaced, mode %v", info.Mode())
	}
	assertDirEntries(t, dir, "link", "victim")
}

func TestWriteRefusesDirectory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	err := atomicfile.Write(t.Context(), sub, strings.NewReader("data"))
	if !errors.Is(err, atomicfile.ErrNotRegular) {
		t.Fatalf("Write() error = %v, want %v", err, atomicfile.ErrNotRegular)
	}
	assertDirEntries(t, dir, "sub")
}

func TestWriteMissingDirectory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	err := atomicfile.Write(t.Context(), filepath.Join(dir, "missing", "out.txt"), strings.NewReader("data"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Write() error = %v, want %v", err, fs.ErrNotExist)
	}
	assertDirEntries(t, dir)
}

func writeFile(t *testing.T, path string, data []byte, mode fs.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	// Chmod sets the exact mode; WriteFile is subject to the umask.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
}

func assertFile(t *testing.T, path string, wantData []byte, wantMode fs.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != wantMode {
		t.Errorf("mode = %v, want %v", got, wantMode)
	}
	if wantMode&0o400 == 0 {
		// Make the file readable to compare it; the mode is already checked.
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatalf("chmod %s: %v", path, err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !bytes.Equal(got, wantData) {
		t.Errorf("content differs: got %d bytes, want %d bytes", len(got), len(wantData))
	}
}

// assertDirEntries checks that dir holds exactly the named entries, so a
// leftover temporary file fails the test.
func assertDirEntries(t *testing.T, dir string, want ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	got := make([]string, 0, len(entries))
	for _, e := range entries {
		got = append(got, e.Name())
	}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("dir entries = %q, want %q", got, want)
	}
}
