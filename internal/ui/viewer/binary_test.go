package viewer

import (
	"bytes"
	"strings"
	"testing"
)

func TestBinaryPreview(t *testing.T) {
	t.Parallel()
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	tests := []struct {
		name    string
		data    []byte
		want    []string
		notWant []string
	}{
		{
			name: "empty",
			data: nil,
			want: []string{"Size:          0 bytes", "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		},
		{
			name:    "png signature",
			data:    png,
			want:    []string{"Size:          16 bytes", "Content type:  image/png", "00000000  89 50 4e 47 0d 0a 1a 0a  00 00 00 0d 49 48 44 52  |.PNG........IHDR|"},
			notWant: []string{"more bytes not shown"},
		},
		{
			name: "bounded",
			data: bytes.Repeat([]byte{0xff}, BinaryPreviewBytes+10),
			want: []string{"... 10 more bytes not shown", "00000ff0  ff"},
			// The dump stops at BinaryPreviewBytes.
			notWant: []string{"00001000  ff"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := BinaryPreview(tt.data)
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("preview lacks %q:\n%s", w, got)
				}
			}
			for _, w := range tt.notWant {
				if strings.Contains(got, w) {
					t.Errorf("preview has %q:\n%s", w, got)
				}
			}
			for i := 0; i < len(got); i++ {
				if c := got[i]; c != '\n' && (c < 0x20 || c > 0x7e) {
					t.Fatalf("preview byte %d is %#x, want printable ASCII", i, c)
				}
			}
		})
	}
}

func TestSetContentRendersBinaryAsPreview(t *testing.T) {
	t.Parallel()
	raw := "\x1b[2J\x00\xff"
	tests := []struct {
		name       string
		mime       string
		wantRawSet bool
	}{
		{"binary mime", BinaryMIME, false},
		{"text", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			w := New("")
			w.SetContent(raw, Metadata{MimeType: tt.mime})
			if got := strings.Contains(w.raw, raw); got != tt.wantRawSet {
				t.Fatalf("raw bytes kept = %v, want %v; content %q", got, tt.wantRawSet, w.raw)
			}
			if !tt.wantRawSet && w.raw != BinaryPreview([]byte(raw)) {
				t.Fatalf("content = %q, want the binary preview", w.raw)
			}
		})
	}
}
