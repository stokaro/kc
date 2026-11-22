package models

import (
	"bytes"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// nonUTF8 holds NUL, invalid UTF-8, a C1 CSI and an ESC: bytes that a text
// path would mangle or send to the terminal as control sequences.
var nonUTF8 = []byte{0x00, 0xff, 0xfe, 0x9b, 0x1b, '[', '2', 'J', 0x00, 0x80}

func TestConfigMapKeyEntries(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		data       map[string]string
		binaryData map[string][]byte
		want       []configMapKeyEntry
	}{
		{name: "empty", want: make([]configMapKeyEntry, 0)},
		{
			name: "text only",
			data: map[string]string{"b": "BB", "a": "A"},
			want: []configMapKeyEntry{{"a", "text", 1}, {"b", "text", 2}},
		},
		{
			name:       "binary only",
			binaryData: map[string][]byte{"blob": nonUTF8},
			want:       []configMapKeyEntry{{"blob", "binary", len(nonUTF8)}},
		},
		{
			name:       "mixed and sorted across fields",
			data:       map[string]string{"z.conf": "zz", "a.conf": ""},
			binaryData: map[string][]byte{"m.bin": {1, 2, 3}},
			want:       []configMapKeyEntry{{"a.conf", "text", 0}, {"m.bin", "binary", 3}, {"z.conf", "text", 2}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := configMapKeyEntries(tt.data, tt.binaryData)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

// configMapObject converts a typed ConfigMap the way the API server
// returns it: binaryData values become base64 strings.
func configMapObject(t *testing.T, cm *corev1.ConfigMap) map[string]any {
	t.Helper()
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(cm)
	if err != nil {
		t.Fatalf("to unstructured: %v", err)
	}
	return obj
}

func TestConfigMapKeyView(t *testing.T) {
	t.Parallel()
	cm := &corev1.ConfigMap{
		Name: "cfg", Namespace: "web",
		Data: map[string]string{
			"app.conf": "key: value\n",
			"esc.txt":  "red \x1b[31m",
		},
		BinaryData: map[string][]byte{
			"logo.png":  {0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0x00},
			"blob.v1.2": nonUTF8,
			"empty.bin": {},
		},
	}
	obj := configMapObject(t, cm)
	tests := []struct {
		key      string
		wantBody []byte
		wantMIME string
	}{
		{key: "app.conf", wantBody: []byte("key: value\n"), wantMIME: ""},
		{key: "esc.txt", wantBody: []byte("red \x1b[31m"), wantMIME: binaryMIME},
		{key: "logo.png", wantBody: cm.BinaryData["logo.png"], wantMIME: binaryMIME},
		{key: "blob.v1.2", wantBody: nonUTF8, wantMIME: binaryMIME},
		{key: "empty.bin", wantBody: make([]byte, 0), wantMIME: binaryMIME},
		{key: "missing", wantBody: make([]byte, 0), wantMIME: ""},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			t.Parallel()
			title, body, _, mime, filename, err := configMapKeyView(obj, "cfg", tt.key)
			if err != nil {
				t.Fatalf("view: %v", err)
			}
			// F5 writes []byte(body), so the body must be the exact value.
			if !bytes.Equal([]byte(body), tt.wantBody) {
				t.Fatalf("body = %q, want %q", body, tt.wantBody)
			}
			if mime != tt.wantMIME {
				t.Fatalf("mime = %q, want %q", mime, tt.wantMIME)
			}
			if title != "cfg:"+tt.key || filename != "cfg_"+tt.key {
				t.Fatalf("title %q filename %q", title, filename)
			}
		})
	}
}

func TestConfigMapKeyViewRefusesBadBase64(t *testing.T) {
	t.Parallel()
	obj := map[string]any{"binaryData": map[string]any{"k": "not base64!"}}
	if _, _, _, _, _, err := configMapKeyView(obj, "cfg", "k"); err == nil {
		t.Fatal("want an error for invalid base64")
	}
}

func TestIsProbablyText(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   []byte
		want bool
	}{
		{"empty", nil, true},
		{"ascii with whitespace", []byte("a\tb\r\nc"), true},
		{"utf-8", []byte("grüße 日本"), true},
		{"nul", []byte("a\x00b"), false},
		{"esc", []byte("\x1b[2J"), false},
		{"del", []byte("a\x7f"), false},
		{"c1 csi", []byte("a\u009b2J"), false},
		{"invalid utf-8", []byte{'a', 0xff}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isProbablyText(tt.in); got != tt.want {
				t.Fatalf("isProbablyText(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}
