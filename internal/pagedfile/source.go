// Package pagedfile reads a large text file in fixed-size blocks for the
// viewer: a bounded block cache, line starts found from any offset, a line
// index built as blocks arrive, and a search that streams over the file.
// See docs/adr/0235-paged-file-viewer.md.
package pagedfile

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
)

// Source is a file read by offset. ReadAt reads up to len(p) bytes at off;
// it returns fewer only at the end of the file, with a nil error or io.EOF.
// Size returns the size of the file as last seen, and whether it is known.
// ReadAt and Size may be called from several goroutines.
type Source interface {
	ReadAt(ctx context.Context, p []byte, off int64) (int, error)
	Size() (int64, bool)
	Close() error
}

// FileSource is a local file.
type FileSource struct {
	file *os.File
}

// NewFileSource reads file; Close closes it.
func NewFileSource(file *os.File) *FileSource { return &FileSource{file: file} }

// ReadAt reads from the file. A canceled ctx stops it before the read.
func (s *FileSource) ReadAt(ctx context.Context, p []byte, off int64) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	n, err := s.file.ReadAt(p, off)
	if err != nil && !errors.Is(err, io.EOF) {
		return n, fmt.Errorf("read %s at %d: %w", s.file.Name(), off, err)
	}
	return n, err
}

// Size stats the file, so a file that grows or shrinks while it is open
// reports its new size.
func (s *FileSource) Size() (int64, bool) {
	info, err := s.file.Stat()
	if err != nil {
		return 0, false
	}
	return info.Size(), true
}

// Close closes the file.
func (s *FileSource) Close() error { return s.file.Close() }

// localSource marks a source that is cheap to read whole.
type localSource interface{ local() }

func (*FileSource) local() {}

// IsLocal reports whether reading all of src is cheap, as for a local
// file. The viewer counts the lines of such a file in the background; for
// a container file every block is a command in the pod, so it does not.
func IsLocal(src Source) bool {
	_, ok := src.(localSource)
	return ok
}
