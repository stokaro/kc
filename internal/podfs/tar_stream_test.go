package podfs

import (
	"strings"
	"testing"
)

func TestTailBufferKeepsTheEnd(t *testing.T) {
	b := &tailBuffer{limit: 8}
	for _, s := range []string{"tar: ", "a: Permission denied\n", "tar: exit 2"} {
		if n, err := b.Write([]byte(s)); err != nil || n != len(s) {
			t.Fatalf("Write(%q) = %d, %v", s, n, err)
		}
	}
	if got := b.String(); got != ": exit 2" {
		t.Fatalf("tail = %q", got)
	}
}

func TestTarStreamNeedsExecConfig(t *testing.T) {
	s := &nativeSession{spec: SessionSpec{Namespace: "ns", Pod: "p", Container: "c"}}
	if _, err := s.TarStream(t.Context(), "/", "etc"); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("TarStream without a config = %v", err)
	}
}
