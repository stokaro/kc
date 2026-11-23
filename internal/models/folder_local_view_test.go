package models_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stokaro/kc/internal/models"
)

// localRow returns the row of a local folder for the entry name.
func localRow(t *testing.T, dir, name string) models.LocalEntryItem {
	t.Helper()
	f := models.NewLocalDirFolder(models.Deps{Ctx: t.Context()}, dir)
	want := filepath.Join(dir, name)
	for _, r := range f.Lines(t.Context(), 0, f.Len(t.Context())) {
		if le, ok := r.(models.LocalEntryItem); ok && le.LocalPath() == want {
			return le
		}
	}
	t.Fatalf("no row for %s", want)
	return nil
}

func writeLocal(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func TestLocalRowsViewContent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeLocal(t, filepath.Join(dir, "notes.txt"), "hello\nworld\n", 0o600)
	writeLocal(t, filepath.Join(dir, "image.bin"), "PNG\x00\x01\x02", 0o600)
	writeLocal(t, filepath.Join(dir, "empty.txt"), "", 0o600)
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "notes.txt"), filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		entry    string
		wantBody string
		wantErr  string
	}{
		{name: "text file", entry: "notes.txt", wantBody: "hello\nworld\n"},
		{name: "empty file", entry: "empty.txt", wantBody: ""},
		{name: "link to a text file", entry: "link", wantBody: "hello\nworld\n"},
		{name: "binary file", entry: "image.bin", wantErr: "is a binary file"},
		{name: "directory", entry: "sub", wantErr: "is a directory; press Enter to open it"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			viewable, ok := localRow(t, dir, tc.entry).(models.Viewable)
			if !ok {
				t.Fatalf("%s row is not viewable", tc.entry)
			}
			title, body, _, _, filename, err := viewable.ViewContent()
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ViewContent: %v", err)
			}
			full := filepath.Join(dir, tc.entry)
			if title != full || filename != tc.entry || body != tc.wantBody {
				t.Fatalf("got title %q, filename %q, body %q", title, filename, body)
			}
		})
	}
}

func TestReadLocalTextFileLimit(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "big.txt")
	writeLocal(t, p, strings.Repeat("a", 100), 0o600)
	data, err := models.ReadLocalTextFile(p, 10)
	if err != nil || string(data) != strings.Repeat("a", 10) {
		t.Fatalf("ReadLocalTextFile = %q, %v", data, err)
	}
}

func TestReadLocalTextFileMissing(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "gone.txt")
	_, err := models.ReadLocalTextFile(p, 10)
	if err == nil || !strings.Contains(err.Error(), "cannot read gone.txt: ") || strings.Contains(err.Error(), p) {
		t.Fatalf("err = %v", err)
	}
}

func TestLocalFileViewCommand(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, file, quoted string
	}{
		{name: "plain path", file: "notes.md", quoted: "notes.md"},
		{name: "quoted path", file: "on call's notes.md", quoted: `on call'\''s notes.md`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			writeLocal(t, filepath.Join(dir, tc.file), "text\n", 0o600)
			row, ok := localRow(t, dir, tc.file).(interface{ ViewCommand() string })
			if !ok {
				t.Fatalf("a local file row has no ViewCommand")
			}
			want := "cat " + filepath.Join(dir, tc.quoted)
			if tc.quoted != tc.file {
				want = "cat '" + filepath.Join(dir, tc.quoted) + "'"
			}
			if got := row.ViewCommand(); got != want {
				t.Fatalf("ViewCommand() = %q, want %q", got, want)
			}
		})
	}
}

func TestCheckLocalEditable(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	text := filepath.Join(dir, "a.txt")
	writeLocal(t, text, "a", 0o600)
	bin := filepath.Join(dir, "a.bin")
	writeLocal(t, bin, "\x00", 0o600)
	readOnly := filepath.Join(dir, "ro.txt")
	writeLocal(t, readOnly, "ro", 0o400)

	if err := models.CheckLocalEditable(text); err != nil {
		t.Fatalf("text file: %v", err)
	}
	if got, err := os.ReadFile(text); err != nil || string(got) != "a" {
		t.Fatalf("the check changed the file: %q, %v", got, err)
	}
	for _, tc := range []struct{ path, want string }{
		{bin, "is a binary file"},
		{dir, "is a directory"},
		{filepath.Join(dir, "missing"), "cannot read"},
	} {
		if err := models.CheckLocalEditable(tc.path); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: err = %v, want %q", tc.path, err, tc.want)
		}
	}
	if os.Geteuid() == 0 {
		t.Skip("root can write a read-only file")
	}
	if err := models.CheckLocalEditable(readOnly); err == nil || !strings.Contains(err.Error(), "cannot write") {
		t.Fatalf("read-only file: err = %v", err)
	}
}
