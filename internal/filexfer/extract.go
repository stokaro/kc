package filexfer

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
)

// extract reads a tar stream and hands its entries to w. Regular files and
// directories are written; symbolic links, devices and pipes are recorded
// as skipped. An unsafe name stops the extraction with ErrUnsafePath.
func extract(ctx context.Context, r io.Reader, w *writer) error {
	tr := tar.NewReader(r)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading archive: %w", err)
		}
		w.entries++
		mode := hdr.FileInfo().Mode().Perm()
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := w.dir(hdr.Name); err != nil {
				return err
			}
		case tar.TypeReg, '\x00': // '\x00' is the old TypeRegA
			if err := w.file(ctx, hdr.Name, mode, hdr.ModTime, tr); err != nil {
				return err
			}
		case tar.TypeLink:
			if err := w.link(ctx, hdr.Name, hdr.Linkname, mode, hdr.ModTime); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if _, err := w.resolve(hdr.Name); err != nil {
				return err
			}
			w.skip(hdr.Name, "symbolic link not copied")
		default:
			if _, err := w.resolve(hdr.Name); err != nil {
				return err
			}
			w.skip(hdr.Name, "special file not copied")
		}
	}
}
