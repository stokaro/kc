package ui

import (
	"bytes"
	"io"
	"testing"
)

// F5 on a binary ConfigMap or Secret key copies the viewer body; the
// written bytes must be the original bytes, not a text rendering.
func TestViewContentCopyRequestKeepsBytes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		data []byte
	}{
		{"empty", []byte{}},
		{"text", []byte("key: value\n")},
		{"nul and invalid utf-8", []byte{0x00, 0xff, 0xfe, 0x9b, 0x1b, '[', '2', 'J', 0x00, 0x80}},
		{"all byte values", allBytes()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := viewContentCopyRequest("cfg:k", "cfg_k", string(tt.data))
			rc, err := req.fetch(t.Context())
			if err != nil {
				t.Fatalf("fetch: %v", err)
			}
			defer rc.Close()
			got, err := io.ReadAll(rc)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if !bytes.Equal(got, tt.data) {
				t.Fatalf("copied %x, want %x", got, tt.data)
			}
		})
	}
}

func allBytes() []byte {
	b := make([]byte, 256)
	for i := range b {
		b[i] = byte(i)
	}
	return b
}
