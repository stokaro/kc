package filexfer

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"time"

	"github.com/stokaro/kc/internal/podfs"
)

// Destination writes into a container filesystem. The pod filesystem
// sessions of podfs implement it.
type Destination interface {
	List(ctx context.Context, dir string) ([]podfs.FileEntry, error)
	podfs.TarExtractor
}

// UploadRequest describes one copy from a local directory into a container
// directory.
type UploadRequest struct {
	// Src is the local directory that holds the entries.
	Src string
	// Names are the entries to copy; each is a single path element.
	Names []string
	// Dir is the absolute container directory the entries go into.
	Dir    string
	Policy Policy
}

// UploadModeMask keeps the permission bits of a local file for its copy in
// the container and drops setuid, setgid and sticky. Programs in the
// container read the files under their own user, so group and other bits
// stay as they are locally.
const UploadModeMask fs.FileMode = 0o777

// Upload copies req.Names from req.Src into req.Dir through dst: one tar
// stream per top-level entry, built here from the local tree, unpacked in
// the container. It returns one Result per entry, in order. The error is
// set only when the copy cannot start at all. Cancel ctx to stop: the entry
// in progress is not moved into place and the rest are reported as
// canceled.
func Upload(ctx context.Context, dst Destination, req UploadRequest, counter *Counter) ([]Result, error) {
	root, err := os.OpenRoot(req.Src)
	if err != nil {
		return nil, fmt.Errorf("opening source: %w", err)
	}
	defer root.Close() //nolint:errcheck // this releases the read-only source directory descriptor
	// The listing decides skip and overwrite now, not when the dialog
	// opened; the container side checks again before it moves an entry.
	listing, err := dst.List(ctx, req.Dir)
	if err != nil {
		return nil, fmt.Errorf("listing %s: %w", req.Dir, err)
	}
	existing := make(map[string]podfs.EntryType, len(listing))
	for _, e := range listing {
		existing[e.Name] = e.Type
	}
	results := make([]Result, 0, len(req.Names))
	var noTar error
	for i, name := range req.Names {
		if ctx.Err() != nil {
			results = append(results, Result{Name: name, Status: StatusCanceled, Message: "not started", Err: ctx.Err()})
			continue
		}
		if noTar != nil {
			// Every entry needs the same tar; do not exec it again.
			results = append(results, Result{Name: name, Status: StatusFailed, Message: noTar.Error(), Err: noTar})
			continue
		}
		counter.startEntry(name, i+1, len(req.Names))
		before := counter.Snapshot()
		res := uploadEntry(ctx, dst, root, req, name, existing, counter)
		after := counter.Snapshot()
		if res.Status == StatusCopied {
			res.Files, res.Bytes = after.Files-before.Files, after.Bytes-before.Bytes
		}
		if errors.Is(res.Err, podfs.ErrTarMissing) {
			noTar = res.Err
		}
		results = append(results, res)
	}
	return results, nil
}

func uploadEntry(ctx context.Context, dst Destination, root *os.Root, req UploadRequest, name string, existing map[string]podfs.EntryType, counter *Counter) Result {
	res := Result{Name: name}
	fail := func(err error) Result {
		res.Err, res.Status, res.Message = err, StatusFailed, err.Error()
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			res.Status = StatusCanceled
		}
		return res
	}
	if !validEntryName(name) {
		return fail(fmt.Errorf("%w: %q", ErrUnsafePath, name))
	}
	info, err := root.Lstat(name)
	if err != nil {
		return fail(err)
	}
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		res.Status, res.Message = StatusSkipped, "symbolic links are not copied"
		return res
	case !info.IsDir() && !info.Mode().IsRegular():
		res.Status, res.Message = StatusSkipped, "special files are not copied"
		return res
	}
	typ, exists := existing[name]
	if exists && req.Policy == SkipExisting {
		res.Status, res.Message = StatusSkipped, "exists"
		return res
	}
	if exists {
		switch {
		case typ == podfs.EntryTypeSymlink:
			return fail(fmt.Errorf("%s in the container: %w", name, ErrSymlink))
		case info.IsDir() && typ != podfs.EntryTypeDir:
			return fail(fmt.Errorf("%s exists in the container and is not a directory", name))
		case !info.IsDir() && typ != podfs.EntryTypeFile:
			return fail(fmt.Errorf("%s in the container: %w", name, ErrNotRegular))
		}
	}

	a := &archiver{root: root, counter: counter}
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := a.write(ctx, pw, name, info)
		pw.CloseWithError(err)
		done <- err
	}()
	extractErr := dst.ExtractTar(ctx, podfs.ExtractRequest{Dir: req.Dir, Name: name, Replace: exists}, pr)
	// Unblock the archiver when the container side stopped reading early.
	_ = pr.CloseWithError(errExtractorGone)
	archiveErr := <-done
	if errors.Is(archiveErr, errExtractorGone) {
		archiveErr = nil
	}
	switch {
	case ctx.Err() != nil:
		return fail(ctx.Err())
	case archiveErr != nil:
		// The local read failed; the container side saw a cut archive and
		// moved nothing into place.
		return fail(archiveErr)
	case extractErr != nil:
		return fail(extractErr)
	}
	res.Status, res.Notes = StatusCopied, notes(a.skipped)
	return res
}

var errExtractorGone = errors.New("the container stopped reading the archive")

// archiver writes one local entry as a tar stream. It reads through root,
// so nothing outside the source directory is read, and it never follows a
// symbolic link: links, devices, pipes and sockets are left out and noted.
type archiver struct {
	root    *os.Root
	counter *Counter
	skipped []string
}

func (a *archiver) write(ctx context.Context, w io.Writer, name string, info fs.FileInfo) error {
	tw := tar.NewWriter(w)
	if err := a.add(ctx, tw, name, info); err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg,
		Name:     podfs.UploadCompleteMarker,
		Mode:     0o600,
		ModTime:  time.Now().Truncate(time.Second),
		Format:   tar.FormatGNU,
	}); err != nil {
		return err
	}
	return tw.Close()
}

// add writes rel, whose Lstat result is info, and its tree.
func (a *archiver) add(ctx context.Context, tw *tar.Writer, rel string, info fs.FileInfo) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		a.skip(rel, "symbolic link not copied")
		return nil
	case info.IsDir():
		return a.addDir(ctx, tw, rel, info)
	case info.Mode().IsRegular():
		return a.addFile(ctx, tw, rel, info)
	default:
		a.skip(rel, "special file not copied")
		return nil
	}
}

func (a *archiver) addDir(ctx context.Context, tw *tar.Writer, rel string, info fs.FileInfo) error {
	d, err := a.open(rel, info)
	if err != nil {
		return err
	}
	entries, err := d.ReadDir(-1)
	_ = d.Close()
	if err != nil {
		return fmt.Errorf("reading %s: %w", rel, err)
	}
	if err := tw.WriteHeader(header(tar.TypeDir, rel+"/", info, 0)); err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, e := range entries {
		child := path.Join(rel, e.Name())
		if !validEntryName(e.Name()) {
			a.skip(child, "name not copied")
			continue
		}
		ci, err := a.root.Lstat(filepath.FromSlash(child))
		if errors.Is(err, fs.ErrNotExist) {
			continue // removed since ReadDir
		}
		if err != nil {
			return err
		}
		if err := a.add(ctx, tw, child, ci); err != nil {
			return err
		}
	}
	return nil
}

func (a *archiver) addFile(ctx context.Context, tw *tar.Writer, rel string, info fs.FileInfo) error {
	f, err := a.open(rel, info)
	if err != nil {
		return err
	}
	defer f.Close() //nolint:errcheck // source is read-only; Stat and CopyN report data errors
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(header(tar.TypeReg, rel, st, st.Size())); err != nil {
		return err
	}
	// A tar member has a fixed size: a file that shrank while it was read
	// fails the entry, one that grew is copied up to its size at open.
	if _, err := io.CopyN(tw, countingReader(ctx, f, a.counter), st.Size()); err != nil {
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("%s changed while it was copied", rel)
		}
		return fmt.Errorf("reading %s: %w", rel, err)
	}
	a.counter.addFile()
	return nil
}

// open opens rel and makes sure it is the file Lstat saw: a file swapped
// for a symbolic link in between is refused instead of followed.
func (a *archiver) open(rel string, info fs.FileInfo) (*os.File, error) {
	f, err := a.root.Open(filepath.FromSlash(rel))
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if !os.SameFile(info, st) {
		_ = f.Close()
		return nil, fmt.Errorf("%s changed while it was copied", rel)
	}
	return f, nil
}

func (a *archiver) skip(name, reason string) {
	a.skipped = append(a.skipped, fmt.Sprintf("%s: %s", name, reason))
}

// header describes a local entry for the container. Owner and group are
// root and unnamed: tar running as root in the container then creates
// files as its own user would, and tar running as another user ignores
// them anyway.
func header(typ byte, name string, info fs.FileInfo, size int64) *tar.Header {
	return &tar.Header{
		Typeflag: typ,
		Name:     name,
		Size:     size,
		Mode:     int64(info.Mode().Perm() & UploadModeMask),
		ModTime:  info.ModTime().Truncate(time.Second),
		Format:   tar.FormatGNU,
	}
}
