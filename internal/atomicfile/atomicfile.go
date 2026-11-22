// Package atomicfile writes local files so that a reader never sees partial
// content and a failed write never damages the file that was there before.
//
// Write streams the content into a private temporary file in the destination
// directory, syncs it, and renames it over the destination only after the
// whole stream was read and written. See docs/adr/0057-atomic-private-exports.md
// for the permission and symlink rules.
package atomicfile

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// DefaultMode is the permission of a file that Write creates. Exports can hold
// decoded Secret values, so only the owner may read or write them.
const DefaultMode fs.FileMode = 0o600

var (
	// ErrSymlink is returned when the destination is a symbolic link. Write does
	// not follow it and does not replace it.
	ErrSymlink = errors.New("destination is a symbolic link")
	// ErrNotRegular is returned when the destination exists and is not a
	// regular file (a directory, a device, a socket or a named pipe).
	ErrNotRegular = errors.New("destination is not a regular file")
	// ErrReadOnly is returned when the destination exists and its owner has no
	// write permission on it.
	ErrReadOnly = errors.New("destination is read-only")
)

// Write copies r into the file at path and replaces path only after the copy
// completed. On any error, including cancellation of ctx, the previous file at
// path is left untouched and the temporary file is removed.
//
// A new file gets DefaultMode. An existing file keeps its owner permission bits
// and loses all group and other bits, so a replacement never widens access.
func Write(ctx context.Context, path string, r io.Reader) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	mode, err := targetMode(path)
	if err != nil {
		return err
	}

	dir, base := filepath.Split(path)
	if dir == "" {
		dir = "."
	}
	// CreateTemp opens the file with O_EXCL and at most 0600, so the partial
	// content is private from the first byte.
	tmp, err := os.CreateTemp(dir, "."+base+".kc-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temporary file: %w", err)
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if committed {
			return
		}
		_ = tmp.Close()
		if rmErr := os.Remove(tmpName); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
			err = errors.Join(err, fmt.Errorf("removing temporary file: %w", rmErr))
		}
	}()

	if _, err := io.Copy(tmp, contextReader(ctx, r)); err != nil {
		return err
	}
	// Set the mode explicitly: the umask may have narrowed the 0600 of
	// CreateTemp, and a replaced file may carry fewer owner bits.
	if err := tmp.Chmod(mode); err != nil {
		return fmt.Errorf("setting permissions: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("syncing temporary file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temporary file: %w", err)
	}
	// Last chance to honor cancellation before the destination changes.
	if err := ctx.Err(); err != nil {
		return err
	}
	// rename(2) replaces a symlink placed at path after the check above instead
	// of following it, so no other file can be overwritten through it.
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replacing destination: %w", err)
	}
	committed = true
	syncDir(dir)
	return nil
}

// targetMode checks the destination and returns the mode the new content gets.
func targetMode(path string) (fs.FileMode, error) {
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return DefaultMode, nil
	case err != nil:
		return 0, err
	}
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		return 0, fmt.Errorf("%s: %w", path, ErrSymlink)
	case !info.Mode().IsRegular():
		return 0, fmt.Errorf("%s: %w", path, ErrNotRegular)
	case info.Mode().Perm()&0o200 == 0:
		return 0, fmt.Errorf("%s: %w", path, ErrReadOnly)
	}
	return info.Mode().Perm() & DefaultMode, nil
}

// syncDir makes the rename durable. It is best effort: the content is already
// in place, and some platforms and file systems cannot sync a directory.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}

// contextReader stops a copy as soon as ctx is done, even when r does not
// watch the context itself.
func contextReader(ctx context.Context, r io.Reader) io.Reader {
	return readerFunc(func(p []byte) (int, error) {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		return r.Read(p)
	})
}

type readerFunc func(p []byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }
