package filexfer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/stokaro/kc/internal/podfs"
)

// Source reads a container filesystem. podfs.ExecSession implements it.
type Source interface {
	List(ctx context.Context, dir string) ([]podfs.FileEntry, error)
	ReadFile(ctx context.Context, file string, limit int64) (io.ReadCloser, error)
}

// TarSource is a Source that can also stream `tar cf -` of one entry.
// Download prefers it and walks the tree through List and ReadFile when
// the stream fails before its first entry (no tar in the image).
type TarSource interface {
	TarStream(ctx context.Context, dir, name string) (io.ReadCloser, error)
}

// Policy decides what happens to a top-level entry that exists locally.
type Policy int

const (
	// SkipExisting leaves an existing local file or directory untouched and
	// reports the entry as skipped.
	SkipExisting Policy = iota
	// Overwrite replaces an existing file. An existing directory is merged:
	// files of the copy replace local files of the same name, other local
	// files stay.
	Overwrite
)

// Request describes one copy from a container directory to a local one.
type Request struct {
	// Dir is the container directory that holds the entries.
	Dir string
	// Entries are the entries to copy; each Name is a single path element.
	Entries []podfs.FileEntry
	// Dest is the local directory the entries are copied into.
	Dest   string
	Policy Policy
}

// Status is the outcome of one top-level entry.
type Status string

const (
	StatusCopied   Status = "copied"
	StatusSkipped  Status = "skipped"
	StatusFailed   Status = "failed"
	StatusCanceled Status = "canceled"
)

// maxNotes bounds the skipped-entry notes kept per result.
const maxNotes = 20

// Result is the outcome of one top-level entry.
type Result struct {
	Name   string
	Status Status
	// Files and Bytes count what was written for this entry. For a failed
	// or canceled entry nothing of it is left in the destination.
	Files int64
	Bytes int64
	// Message explains a skip or a failure.
	Message string
	// Notes lists entries inside the copy that were left out on purpose,
	// such as symbolic links.
	Notes []string
	Err   error
}

// Download copies req.Entries from src into req.Dest. It returns one
// Result per entry, in order. The error is set only when the destination
// cannot be used at all. Cancel ctx to stop: the entry in progress is
// removed and the rest are reported as canceled.
func Download(ctx context.Context, src Source, req Request, counter *Counter) ([]Result, error) {
	root, err := os.OpenRoot(req.Dest)
	if err != nil {
		return nil, fmt.Errorf("opening destination: %w", err)
	}
	defer root.Close()
	results := make([]Result, 0, len(req.Entries))
	for i, e := range req.Entries {
		if ctx.Err() != nil {
			results = append(results, Result{Name: e.Name, Status: StatusCanceled, Message: "not started", Err: ctx.Err()})
			continue
		}
		counter.startEntry(e.Name, i+1, len(req.Entries))
		before := counter.Snapshot()
		res := downloadEntry(ctx, src, root, req, e, counter)
		after := counter.Snapshot()
		if res.Status == StatusCopied {
			res.Files, res.Bytes = after.Files-before.Files, after.Bytes-before.Bytes
		}
		results = append(results, res)
	}
	return results, nil
}

func downloadEntry(ctx context.Context, src Source, root *os.Root, req Request, e podfs.FileEntry, counter *Counter) Result {
	res := Result{Name: e.Name}
	fail := func(err error, staging string) Result {
		res.Err = err
		res.Status = StatusFailed
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			res.Status = StatusCanceled
		}
		msg := err.Error()
		if staging != "" {
			msg = strings.ReplaceAll(msg, staging, e.Name)
		}
		res.Message = msg
		return res
	}
	if !validEntryName(e.Name) {
		return fail(fmt.Errorf("%w: %q", ErrUnsafePath, e.Name), "")
	}
	switch e.Type {
	case podfs.EntryTypeDir, podfs.EntryTypeFile:
	case podfs.EntryTypeSymlink:
		res.Status, res.Message = StatusSkipped, "symbolic links are not copied"
		return res
	default:
		res.Status, res.Message = StatusSkipped, "special files are not copied"
		return res
	}
	info, statErr := root.Lstat(e.Name)
	exists := statErr == nil
	if statErr != nil && !errors.Is(statErr, fs.ErrNotExist) {
		return fail(statErr, "")
	}
	if exists && req.Policy == SkipExisting {
		res.Status, res.Message = StatusSkipped, "exists"
		return res
	}
	if exists && info.Mode()&fs.ModeSymlink != 0 {
		return fail(fmt.Errorf("%s: %w", e.Name, ErrSymlink), "")
	}

	if e.Type == podfs.EntryTypeFile {
		if exists && !info.Mode().IsRegular() {
			return fail(fmt.Errorf("%s: %w", e.Name, ErrNotRegular), "")
		}
		w := &writer{root: root, top: e.Name, as: e.Name, counter: counter}
		if err := produce(ctx, src, req.Dir, e, w); err != nil {
			return fail(err, "")
		}
		res.Status, res.Notes = StatusCopied, notes(w.skipped)
		return res
	}

	skipped, staging, err := downloadDir(ctx, src, root, req.Dir, e, counter, info, exists)
	if err != nil {
		return fail(err, staging)
	}
	res.Status, res.Notes = StatusCopied, notes(skipped)
	return res
}

// downloadDir copies the directory e. It is built in a private staging
// directory next to the destination and moved into place only when it is
// complete, so a failed or canceled copy leaves nothing that looks finished.
// It returns the staging name, which error messages replace with e.Name.
func downloadDir(ctx context.Context, src Source, root *os.Root, dir string, e podfs.FileEntry, counter *Counter, info fs.FileInfo, exists bool) (skipped []string, staging string, err error) {
	if exists && !info.IsDir() {
		return nil, "", fmt.Errorf("%s: exists and is not a directory", e.Name)
	}
	staging = "." + e.Name + ".kc-partial-" + randomSuffix()
	if err := root.Mkdir(staging, DirMode); err != nil {
		return nil, "", fmt.Errorf("creating staging directory: %w", err)
	}
	defer func() { _ = root.RemoveAll(staging) }()
	w := &writer{root: root, top: e.Name, as: staging, counter: counter}
	if err := produce(ctx, src, dir, e, w); err != nil {
		return nil, staging, err
	}
	if err := ctx.Err(); err != nil {
		return nil, staging, err
	}
	if err := commit(root, staging, e.Name, exists); err != nil {
		return nil, staging, err
	}
	return w.skipped, staging, nil
}

// produce feeds one entry to w: from a tar stream when src has one, else by
// walking the tree.
func produce(ctx context.Context, src Source, dir string, e podfs.FileEntry, w *writer) error {
	if ts, ok := src.(TarSource); ok {
		err := extractStream(ctx, ts, dir, e.Name, w)
		if err == nil {
			return nil
		}
		// The stream failed before its first entry, so nothing was
		// written: most likely the image has no tar. Walk instead.
		if w.entries > 0 || ctx.Err() != nil {
			return err
		}
		ctrllog.FromContext(ctx).WithName("filexfer").Info("tar stream failed; copying through the filesystem session", "dir", dir, "name", e.Name, "err", err)
	}
	return walk(ctx, src, dir, e, e.Name, w)
}

func extractStream(ctx context.Context, ts TarSource, dir, name string, w *writer) error {
	rc, err := ts.TarStream(ctx, dir, name)
	if err != nil {
		return err
	}
	defer rc.Close()
	if err := extract(ctx, rc, w); err != nil {
		return err
	}
	// Read to the end: tar reports a failure (an unreadable file, say) by
	// its exit status, which arrives after the end-of-archive marker.
	_, err = io.Copy(io.Discard, rc)
	return err
}

// walk copies e, found in the container directory dir, through List and
// ReadFile. archName is the name the entry has in the copy.
func walk(ctx context.Context, src Source, dir string, e podfs.FileEntry, archName string, w *writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p := path.Join(dir, e.Name)
	switch e.Type {
	case podfs.EntryTypeDir:
		if err := w.dir(archName); err != nil {
			return err
		}
		children, err := src.List(ctx, p)
		if err != nil {
			return fmt.Errorf("listing %s: %w", p, err)
		}
		sort.Slice(children, func(i, j int) bool { return children[i].Name < children[j].Name })
		for _, c := range children {
			if !validEntryName(c.Name) {
				return fmt.Errorf("%w: %q in %s", ErrUnsafePath, c.Name, p)
			}
			if err := walk(ctx, src, p, c, archName+"/"+c.Name, w); err != nil {
				return err
			}
		}
		return nil
	case podfs.EntryTypeFile:
		rc, err := src.ReadFile(ctx, p, 0)
		if err != nil {
			return fmt.Errorf("reading %s: %w", p, err)
		}
		err = w.file(ctx, archName, fs.FileMode(e.Mode).Perm(), e.UpdatedAt, rc)
		if err != nil && ctx.Err() == nil {
			// Keep the session's command stream in step: the rest of the
			// file is still on its way.
			_, _ = io.Copy(io.Discard, rc)
		}
		_ = rc.Close()
		if err != nil {
			return fmt.Errorf("copying %s: %w", p, err)
		}
		return nil
	case podfs.EntryTypeSymlink:
		w.skip(archName, "symbolic link not copied")
		return nil
	default:
		w.skip(archName, "special file not copied")
		return nil
	}
}

// commit moves a complete staging directory to name. When name exists
// (Overwrite), the staged tree is merged into it file by file.
func commit(root *os.Root, staging, name string, exists bool) error {
	if !exists {
		return root.Rename(staging, name)
	}
	return fs.WalkDir(root.FS(), filepath.ToSlash(staging), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(p, filepath.ToSlash(staging)), "/")
		target := filepath.FromSlash(path.Join(name, rel))
		if d.IsDir() {
			info, err := root.Lstat(target)
			switch {
			case errors.Is(err, fs.ErrNotExist):
				return root.Mkdir(target, DirMode)
			case err != nil:
				return err
			case info.Mode()&fs.ModeSymlink != 0:
				return fmt.Errorf("%s: %w", target, ErrSymlink)
			case !info.IsDir():
				return fmt.Errorf("%s: exists and is not a directory", target)
			}
			return nil
		}
		if err := checkReplaceable(root, target); err != nil {
			return err
		}
		return root.Rename(filepath.FromSlash(p), target)
	})
}

// validEntryName accepts a single path element that stays in its directory.
func validEntryName(name string) bool {
	return name != "" && name != "." && name != ".." &&
		!strings.ContainsAny(name, "/\\\x00") && filepath.IsLocal(name)
}

func notes(skipped []string) []string {
	if len(skipped) <= maxNotes {
		return skipped
	}
	out := append([]string(nil), skipped[:maxNotes]...)
	return append(out, fmt.Sprintf("and %d more", len(skipped)-maxNotes))
}
