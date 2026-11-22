package models

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	table "github.com/stokaro/kc/internal/table"
)

// LocalPathPrefix starts the panel path of a local directory, so it never
// reads like a cluster path.
const LocalPathPrefix = "local:"

// LocalDirFolder lists a directory of the local filesystem. kc does not
// change it except through a copy: it is the destination of a copy from a
// container and the source of a copy into one.
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
	for _, p := range strings.Split(filepath.ToSlash(dir), "/") {
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
	for i := len(chain) - 1; i >= 0; i-- {
		out = append(out, NewLocalDirFolder(deps, chain[i]))
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

var (
	_ Enterable      = (*LocalDirItem)(nil)
	_ LocalEntryItem = (*LocalDirItem)(nil)
	_ LocalEntryItem = (*LocalFileItem)(nil)
)
