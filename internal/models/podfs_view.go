package models

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"sync/atomic"

	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/stokaro/kc/internal/podfs"
)

// OpenFileView opens the file for F3. A regular file larger than
// ContainerViewWholeBytes opens in pages: the viewer reads one block per
// command on the filesystem session. Smaller files, and links and special
// files, are read whole as ViewContent reads them.
func (i *PodFSFileItem) OpenFileView(context.Context) (FileView, error) {
	folder := i.folder
	if folder == nil || folder.session == nil {
		return FileView{}, errors.New("pod filesystem unavailable")
	}
	view := FileView{Title: i.fsPath, Filename: path.Base(i.fsPath), Size: i.entry.Size}
	if i.entry.Type != podfs.EntryTypeFile || i.entry.Size <= ContainerViewWholeBytes {
		limit := int64(ContainerViewWholeBytes)
		if i.entry.Type == podfs.EntryTypeFile {
			// One byte more: a file that grew since the listing opens
			// in pages rather than cut short.
			limit++
		}
		// The session timeouts bound the read, as for ViewContent.
		data, err := folder.readFileContent(folder.Deps.Ctx, i.fsPath, limit)
		if err != nil {
			return FileView{}, err
		}
		if int64(len(data)) <= ContainerViewWholeBytes || i.entry.Type != podfs.EntryTypeFile {
			view.Body = string(data)
			view.Size = int64(len(data))
			return view, nil
		}
	}
	src := &podFSSource{folder: folder, fsPath: i.fsPath}
	src.size.Store(view.Size)
	view.Source = src
	return view, nil
}

// ContentPath returns the path of the file in the container.
func (i *PodFSFileItem) ContentPath() string { return i.fsPath }

// OpenContent streams the whole file for an F5 export. The reader holds
// the filesystem session until it is closed.
func (i *PodFSFileItem) OpenContent(ctx context.Context) (io.ReadCloser, error) {
	folder := i.folder
	if folder == nil || folder.session == nil {
		return nil, errors.New("pod filesystem unavailable")
	}
	spec := folder.session.spec
	ctrllog.FromContext(ctx).WithName("podfs_export").Info("Reading a container file for export",
		"namespace", spec.Namespace, "pod", spec.Pod, "container", spec.Container, "path", i.fsPath)
	sessionCtx, cancel := context.WithTimeout(ctx, fsSessionTimeout)
	defer cancel()
	sess, release, err := folder.session.acquire(sessionCtx)
	if err != nil {
		return nil, err
	}
	rc, err := sess.ReadFile(ctx, i.fsPath, 0)
	if err != nil {
		release()
		if isTransientSessionError(err) {
			folder.session.Invalidate()
		}
		return nil, err
	}
	return &releasingReader{ReadCloser: rc, release: release}, nil
}

type releasingReader struct {
	io.ReadCloser
	release func()
}

func (r *releasingReader) Close() error {
	err := r.ReadCloser.Close()
	r.release()
	return err
}

// podFSSource reads a container file by offset through the folder's
// filesystem session. Each read is one dd command on the session's shell;
// the session's exec stream is opened once, not per read.
type podFSSource struct {
	folder *PodContainerFSFolder
	fsPath string
	size   atomic.Int64
}

func (s *podFSSource) Size() (int64, bool) { return s.size.Load(), true }

func (s *podFSSource) Close() error { return nil }

// ReadAt reads len(p) bytes at off, fewer at the end of the file.
func (s *podFSSource) ReadAt(ctx context.Context, p []byte, off int64) (int, error) {
	handle := s.folder.session
	if handle == nil {
		return 0, errors.New("pod filesystem unavailable")
	}
	aligned := off - off%podfs.RangeAlign
	skip := off - aligned
	spec := handle.spec
	ctrllog.FromContext(ctx).WithName("podfs_view").Info("Reading a container file block",
		"namespace", spec.Namespace, "pod", spec.Pod, "container", spec.Container,
		"path", s.fsPath, "offset", off, "length", len(p))
	var lastErr error
	for range 2 {
		data, total, err := s.readRange(ctx, aligned, int64(len(p))+skip)
		if err != nil {
			if (isTransientSessionError(err) || isContextError(err)) && ctx.Err() == nil {
				handle.Invalidate()
				lastErr = err
				continue
			}
			return 0, err
		}
		s.size.Store(total)
		if int64(len(data)) <= skip {
			return 0, io.EOF
		}
		n := copy(p, data[skip:])
		if n < len(p) {
			return n, io.EOF
		}
		return n, nil
	}
	return 0, fmt.Errorf("read %s at %d: %w", s.fsPath, off, lastErr)
}

func (s *podFSSource) readRange(ctx context.Context, offset, length int64) ([]byte, int64, error) {
	sessionCtx, cancelSession := context.WithTimeout(ctx, fsSessionTimeout)
	defer cancelSession()
	sess, release, err := s.folder.session.acquire(sessionCtx)
	if err != nil {
		return nil, 0, err
	}
	defer release()
	rr, ok := sess.(podfs.RangeReader)
	if !ok {
		return nil, 0, errors.New("the filesystem session cannot read part of a file")
	}
	readCtx, cancelRead := context.WithTimeout(ctx, fsDefaultTimeout)
	defer cancelRead()
	rc, total, err := rr.ReadFileRange(readCtx, s.fsPath, offset, length)
	if err != nil {
		return nil, 0, err
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, 0, err
	}
	return data, total, nil
}

var (
	_ PagedViewable = (*PodFSFileItem)(nil)
	_ ContentOpener = (*PodFSFileItem)(nil)
)
