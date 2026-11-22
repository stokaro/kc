package atomicfile

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// WriteIn is Write for a name under root. Every path operation goes through
// root, so neither name nor a symbolic link on the way can reach outside it.
//
// The new content gets exactly mode (permission bits only), whatever the
// replaced file had: the caller decides, for example from the mode of the
// file a copy comes from. The destination checks are those of Write: a
// symbolic link, a non-regular file and a file without owner write
// permission are refused and left alone.
func WriteIn(ctx context.Context, root *os.Root, name string, mode fs.FileMode, r io.Reader) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := CheckReplaceable(root, name); err != nil {
		return err
	}
	dir, base := filepath.Split(name)
	tmpName := filepath.Join(dir, "."+base+".kc-"+randomSuffix()+".tmp")
	// O_EXCL with 0600: the partial content is private from the first byte
	// and never lands on a file someone placed there.
	tmp, err := root.OpenFile(tmpName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("creating temporary file: %w", err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		_ = tmp.Close()
		if rmErr := root.Remove(tmpName); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
			err = errors.Join(err, fmt.Errorf("removing temporary file: %w", rmErr))
		}
	}()

	if _, err := io.Copy(tmp, contextReader(ctx, r)); err != nil {
		return err
	}
	if err := tmp.Chmod(mode.Perm()); err != nil {
		return fmt.Errorf("setting permissions: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("syncing temporary file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temporary file: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// rename(2) replaces a symlink placed at name after the check above
	// instead of following it.
	if err := root.Rename(tmpName, name); err != nil {
		return fmt.Errorf("replacing destination: %w", err)
	}
	committed = true
	if dir == "" {
		dir = "."
	}
	if d, err := root.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// CheckReplaceable reports whether WriteIn may replace name under root: it
// refuses a symbolic link, a non-regular file and a file without owner
// write permission. A missing name is fine.
func CheckReplaceable(root *os.Root, name string) error {
	info, err := root.Lstat(name)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return err
	case info.Mode()&fs.ModeSymlink != 0:
		return fmt.Errorf("%s: %w", name, ErrSymlink)
	case !info.Mode().IsRegular():
		return fmt.Errorf("%s: %w", name, ErrNotRegular)
	case info.Mode().Perm()&0o200 == 0:
		return fmt.Errorf("%s: %w", name, ErrReadOnly)
	}
	return nil
}

func randomSuffix() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
