package models

import (
	"context"
	"io"

	"github.com/stokaro/kc/internal/pagedfile"
)

// FileView is what F3 shows of a file. A small file comes whole in Body
// and keeps syntax highlighting; a larger one comes as a Source the viewer
// reads in blocks.
type FileView struct {
	Title    string
	Filename string
	Body     string
	// Source is nil when Body holds the whole file. The viewer closes it.
	Source pagedfile.Source
	// Size is the size of the file when it was opened.
	Size int64
}

// PagedViewable is a file that F3 shows whole when it is small and in
// pages when it is large.
type PagedViewable interface {
	OpenFileView(ctx context.Context) (FileView, error)
}

// ContentOpener streams the whole content of a file for an F5 export.
type ContentOpener interface {
	// ContentPath is the path of the file, for the export's name.
	ContentPath() string
	OpenContent(ctx context.Context) (io.ReadCloser, error)
}

const (
	// LocalViewWholeBytes is the largest local file F3 reads whole, with
	// syntax highlighting. Larger files open in pages.
	LocalViewWholeBytes = 2 << 20
	// ContainerViewWholeBytes is the same limit for a container file.
	ContainerViewWholeBytes = fsFileViewMaxBytes
)
