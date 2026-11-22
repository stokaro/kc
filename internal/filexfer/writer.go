// Package filexfer copies files and directory trees between a container
// and a local directory.
//
// Download: two producers feed the same writer: a tar stream (Extract), as
// kubectl cp reads it from `tar cf -` in the container, and a walk over a
// pod filesystem session (Walk), for images without tar and for the debug
// helper. The writer owns every safety rule, so both paths refuse the same
// names: absolute paths, "..", entries outside the copied top-level name,
// and writes through symbolic links. It never creates a symbolic link or a
// device.
//
// Upload: an archiver builds the tar stream from the local tree through an
// os.Root, never following a symbolic link, and a podfs session unpacks it
// in the container. See
// docs/adr/0073-copy-between-local-and-container-folders.md.
package filexfer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/stokaro/kc/internal/atomicfile"
)

// FileModeMask keeps the owner permission bits of the source and drops the
// group and other bits, setuid, setgid and sticky. A copy is never more
// open than its source, and never readable by other local users.
const FileModeMask fs.FileMode = 0o700

// DirMode is the mode of every directory the copy creates.
const DirMode fs.FileMode = 0o700

var (
	// ErrUnsafePath is returned for an entry name that would leave the
	// destination: absolute, with "..", or outside the copied entry.
	ErrUnsafePath = errors.New("unsafe path")
	// ErrSymlink is returned when a path to be written is a symbolic link.
	ErrSymlink = atomicfile.ErrSymlink
	// ErrNotRegular is returned when a path to be replaced is not a regular
	// file.
	ErrNotRegular = atomicfile.ErrNotRegular
	// ErrReadOnly is returned when a local file to be replaced has no owner
	// write permission.
	ErrReadOnly = atomicfile.ErrReadOnly
)

// writer places the entries of one copied top-level name under root.
// Archive names start with top; the writer stores them under as instead,
// which is either top itself or a private staging directory.
type writer struct {
	root    *os.Root
	top     string
	as      string
	counter *Counter
	// entries counts the archive headers read. A tar stream that fails
	// before the first one may be retried another way.
	entries int
	skipped []string
}

// resolve maps an archive name to a local name under root and refuses
// anything that could leave the copied entry.
func (w *writer) resolve(name string) (string, error) {
	clean, err := cleanArchiveName(name)
	if err != nil {
		return "", err
	}
	first, rest, _ := strings.Cut(clean, "/")
	if first != w.top {
		return "", fmt.Errorf("%w: %q is outside %q", ErrUnsafePath, name, w.top)
	}
	local := w.as
	if rest != "" {
		local = path.Join(w.as, rest)
	}
	local = filepath.FromSlash(local)
	if !filepath.IsLocal(local) {
		return "", fmt.Errorf("%w: %q", ErrUnsafePath, name)
	}
	return local, nil
}

// cleanArchiveName drops a leading "./" and a trailing "/" and refuses
// absolute names, empty names and any ".." element. It does not use
// path.Clean on purpose: "a/../b" is refused, not rewritten to "b".
func cleanArchiveName(name string) (string, error) {
	n := name
	for strings.HasPrefix(n, "./") {
		n = n[2:]
	}
	n = strings.TrimRight(n, "/")
	switch {
	case n == "" || n == ".":
		return "", fmt.Errorf("%w: empty name %q", ErrUnsafePath, name)
	case strings.HasPrefix(n, "/"):
		return "", fmt.Errorf("%w: absolute name %q", ErrUnsafePath, name)
	case strings.ContainsRune(n, 0) || strings.Contains(n, `\`):
		return "", fmt.Errorf("%w: %q", ErrUnsafePath, name)
	}
	for _, elem := range strings.Split(n, "/") {
		if elem == ".." {
			return "", fmt.Errorf("%w: %q has ..", ErrUnsafePath, name)
		}
	}
	return n, nil
}

// dir creates a directory. The mode is DirMode whatever the source says, so
// the copy can always write into it.
func (w *writer) dir(name string) error {
	local, err := w.resolve(name)
	if err != nil {
		return err
	}
	if info, err := w.root.Lstat(local); err == nil {
		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s: %w", local, ErrSymlink)
		}
		if !info.IsDir() {
			return fmt.Errorf("%s: exists and is not a directory", local)
		}
		return nil
	}
	return w.root.MkdirAll(local, DirMode)
}

// file writes r to name. The content goes to a private temporary file next
// to the destination, which replaces the destination only when the whole
// content arrived.
func (w *writer) file(ctx context.Context, name string, mode fs.FileMode, mtime time.Time, r io.Reader) error {
	local, err := w.resolve(name)
	if err != nil {
		return err
	}
	if err := w.ensureParent(local); err != nil {
		return err
	}
	if err := writeFile(ctx, w.root, local, mode, mtime, r, w.counter); err != nil {
		return err
	}
	w.counter.addFile()
	return nil
}

// link copies the content of an already written entry, for a tar hard link.
func (w *writer) link(ctx context.Context, name, target string, mode fs.FileMode, mtime time.Time) error {
	src, err := w.resolve(target)
	if err != nil {
		return err
	}
	f, err := w.root.Open(src)
	if err != nil {
		w.skip(name, "hard link target missing")
		return nil
	}
	defer f.Close()
	return w.file(ctx, name, mode, mtime, f)
}

// skip records an entry the copy leaves out on purpose.
func (w *writer) skip(name, reason string) {
	w.skipped = append(w.skipped, fmt.Sprintf("%s: %s", strings.TrimPrefix(name, "./"), reason))
}

// ensureParent creates the parent directories of local. The writer never
// creates a symbolic link, and a directory entry is always built in a fresh
// staging directory, so no parent can be a link; os.Root refuses any path
// that would leave the destination regardless.
func (w *writer) ensureParent(local string) error {
	parent := filepath.Dir(local)
	if parent == "." {
		return nil
	}
	return w.root.MkdirAll(parent, DirMode)
}

// writeFile writes r to name under root through atomicfile: a private
// temporary file next to the destination that replaces it only when the
// whole content arrived.
func writeFile(ctx context.Context, root *os.Root, name string, mode fs.FileMode, mtime time.Time, r io.Reader, counter *Counter) error {
	if err := atomicfile.WriteIn(ctx, root, name, mode.Perm()&FileModeMask, countingReader(ctx, r, counter)); err != nil {
		return err
	}
	if !mtime.IsZero() {
		_ = root.Chtimes(name, mtime, mtime)
	}
	return nil
}

// checkReplaceable refuses to replace a symbolic link, a non-regular file
// or a read-only file.
func checkReplaceable(root *os.Root, name string) error {
	return atomicfile.CheckReplaceable(root, name)
}

func randomSuffix() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// countingReader stops a copy as soon as ctx is done, even when r does not
// watch the context, and counts the bytes that went through.
func countingReader(ctx context.Context, r io.Reader, counter *Counter) io.Reader {
	return readerFunc(func(p []byte) (int, error) {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		n, err := r.Read(p)
		counter.addBytes(int64(n))
		return n, err
	})
}

type readerFunc func(p []byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }
