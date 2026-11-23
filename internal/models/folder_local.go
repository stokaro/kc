package models

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	table "github.com/stokaro/kc/internal/table"
)

// LocalPathPrefix starts the panel path of a local directory, so it never
// reads like a cluster path.
const LocalPathPrefix = "local:"

// LocalDirFolder lists a directory of the local filesystem. kc changes it
// only through a copy and through the editor F4 opens on a file: it is the
// destination of a copy from a container and the source of a copy into one.
type LocalDirFolder struct {
	*BaseFolder
	dir string
}

// NewLocalDirFolder lists dir, which is made absolute.
func NewLocalDirFolder(deps Deps, dir string) *LocalDirFolder {
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	dir = filepath.Clean(dir)
	f := &LocalDirFolder{
		BaseFolder: NewBaseFolder(deps, []table.Column{
			{Title: " Name"},
			{Title: " Size"},
			{Title: " Mode"},
			{Title: " Modified"},
		}, localFolderPath(dir)),
		dir: dir,
	}
	f.SetPopulate(f.populate)
	return f
}

func localFolderPath(dir string) []string {
	parts := []string{"local"}
	for p := range strings.SplitSeq(filepath.ToSlash(dir), "/") {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return parts
}

// LocalDir returns the absolute directory the folder lists.
func (f *LocalDirFolder) LocalDir() string { return f.dir }

// DisplayPath is the path shown in the panel frame.
func (f *LocalDirFolder) DisplayPath() string { return LocalPathPrefix + f.dir }

// LocalAncestors returns the folders from the filesystem root down to dir,
// so Back walks up the directory tree and leaves it at the root.
func LocalAncestors(deps Deps, dir string) []*LocalDirFolder {
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	dir = filepath.Clean(dir)
	var chain []string
	for d := dir; ; d = filepath.Dir(d) {
		chain = append(chain, d)
		if filepath.Dir(d) == d {
			break
		}
	}
	out := make([]*LocalDirFolder, 0, len(chain))
	for _, c := range slices.Backward(chain) {
		out = append(out, NewLocalDirFolder(deps, c))
	}
	return out
}

func (f *LocalDirFolder) populate(context.Context) ([]table.Row, error) {
	entries, err := os.ReadDir(f.dir)
	if err != nil {
		item := NewSimpleItem("local_error", []string{"error", "", "", ""}, f.Path(), DimStyle())
		item.RowItem.details = fmt.Sprintf("Read %s failed: %v", f.dir, err)
		return []table.Row{item}, nil
	}
	type row struct {
		name string
		info fs.FileInfo
		dir  bool // a directory, or a link to one
	}
	rows := make([]row, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue // removed since ReadDir
		}
		isDir := info.IsDir()
		if info.Mode()&fs.ModeSymlink != 0 {
			if target, err := os.Stat(filepath.Join(f.dir, e.Name())); err == nil && target.IsDir() {
				isDir = true
			}
		}
		rows = append(rows, row{name: e.Name(), info: info, dir: isDir})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].dir != rows[j].dir {
			return rows[i].dir
		}
		return rows[i].name < rows[j].name
	})
	out := make([]table.Row, 0, len(rows))
	for _, r := range rows {
		full := filepath.Join(f.dir, r.name)
		link := r.info.Mode()&fs.ModeSymlink != 0
		cells := []string{localDisplayName(r.name, r.dir, link), "", r.info.Mode().String(), formatModTime(r.info.ModTime())}
		rowPath := append(f.Path(), r.name)
		if !r.dir {
			cells[1] = formatBytes(r.info.Size())
			item := &LocalFileItem{SimpleItem: NewSimpleItem(full, cells, rowPath, WhiteStyle()), path: full}
			item.RowItem.details = full
			out = append(out, item)
			continue
		}
		item := &LocalDirItem{RowItem: NewRowItem(full, cells, rowPath, WhiteStyle()), dir: full, deps: f.Deps}
		item.RowItem.details = full
		out = append(out, item)
	}
	return out, nil
}

// localDisplayName marks directories and links the way mc does: "/" for a
// directory, "~" for a link to a directory, "@" for any other link.
func localDisplayName(name string, dir, link bool) string {
	switch {
	case dir && link:
		return "~" + name
	case dir:
		return "/" + name
	case link:
		return "@" + name
	}
	return name
}

// LocalDirItem is a directory row of a local folder.
type LocalDirItem struct {
	*RowItem
	dir  string
	deps Deps
}

// Enter lists the directory.
func (d *LocalDirItem) Enter() (Folder, error) { return NewLocalDirFolder(d.deps, d.dir), nil }

// LocalPath returns the absolute path of the directory.
func (d *LocalDirItem) LocalPath() string { return d.dir }

// LocalEntryItem is a row of a local folder that can be copied into a
// container.
type LocalEntryItem interface {
	Item
	LocalPath() string
}

// LocalFileItem is a row of a local folder that is not a directory: a
// file, a link to one, or a special file.
type LocalFileItem struct {
	*SimpleItem
	path string
}

// LocalPath returns the absolute path of the entry.
func (f *LocalFileItem) LocalPath() string { return f.path }

// ViewContent reads the file for the viewer (F3). It refuses a directory, a
// special file and a binary file with an error that says why.
//
//revive:disable-next-line:function-result-limit the Viewable interface fixes these six results; internal/ui calls it
func (f *LocalFileItem) ViewContent() (title, body, lang, mime, filename string, err error) {
	data, err := ReadLocalTextFile(f.path, localViewMaxBytes)
	if err != nil {
		return "", "", "", "", "", err
	}
	return f.path, string(data), "", "", filepath.Base(f.path), nil
}

// ViewContent refuses: a directory is opened with Enter, not viewed.
//
//revive:disable-next-line:function-result-limit the Viewable interface fixes these six results; internal/ui calls it
func (d *LocalDirItem) ViewContent() (title, body, lang, mime, filename string, err error) {
	return "", "", "", "", "", localDirError(d.dir)
}

// localViewMaxBytes is how much of a local file the viewer shows, the same
// limit as for a container file.
const localViewMaxBytes = fsFileViewMaxBytes

// localBinarySniffBytes is how much of a file is checked for a NUL byte,
// the test git and grep use to call a file binary.
const localBinarySniffBytes = 8000

func localDirError(p string) error {
	return fmt.Errorf("%s is a directory; press Enter to open it", filepath.Base(p))
}

// localRegularFile checks that p names a regular file, following links.
// Reading a FIFO or a device could block or never end.
func localRegularFile(p string) error {
	info, err := os.Stat(p)
	if err != nil {
		return fmt.Errorf("cannot read %s: %w", filepath.Base(p), pathErrReason(err))
	}
	switch {
	case info.IsDir():
		return localDirError(p)
	case !info.Mode().IsRegular():
		return fmt.Errorf("%s is not a regular file (%s)", filepath.Base(p), info.Mode().Type())
	}
	return nil
}

// pathErrReason drops the path from err: the messages name the entry, and
// a toast cuts a long path short before the reason.
func pathErrReason(err error) error {
	if pe, ok := errors.AsType[*fs.PathError](err); ok {
		return pe.Err
	}
	return err
}

func localBinaryError(p string) error {
	return fmt.Errorf("%s is a binary file", filepath.Base(p))
}

// ReadLocalTextFile reads up to limit bytes of the text file p. It fails
// with a message for the user, naming the entry by its base name, when p
// cannot be read, is a directory or a special file, or holds a NUL byte in
// its first bytes.
func ReadLocalTextFile(p string, limit int64) ([]byte, error) {
	if err := localRegularFile(p); err != nil {
		return nil, err
	}
	file, err := os.Open(p) // #nosec G304 -- the user picked this file in the local panel
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", filepath.Base(p), pathErrReason(err))
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, limit))
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", filepath.Base(p), pathErrReason(err))
	}
	if bytes.IndexByte(data[:min(len(data), localBinarySniffBytes)], 0) >= 0 {
		return nil, localBinaryError(p)
	}
	return data, nil
}

// CheckLocalEditable reports why the editor should not open p: it cannot
// be read or written, or it is not a regular text file.
func CheckLocalEditable(p string) error {
	if _, err := ReadLocalTextFile(p, localBinarySniffBytes); err != nil {
		return err
	}
	// O_WRONLY without O_TRUNC leaves the content alone.
	file, err := os.OpenFile(p, os.O_WRONLY, 0) // #nosec G304 -- the user picked this file in the local panel
	if err != nil {
		return fmt.Errorf("cannot write %s: %w", filepath.Base(p), pathErrReason(err))
	}
	return file.Close()
}

var (
	_ Enterable      = (*LocalDirItem)(nil)
	_ LocalEntryItem = (*LocalDirItem)(nil)
	_ LocalEntryItem = (*LocalFileItem)(nil)
	_ Viewable       = (*LocalDirItem)(nil)
	_ Viewable       = (*LocalFileItem)(nil)
)
