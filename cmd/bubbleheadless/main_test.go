package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/taigrr/bubbleterm/emulator"
)

// TestChildEnvHidesTheOuterTerminal: the child sees the emulator, not the
// terminal bubbleheadless runs in, so kc's auto mode draws text charts.
func TestChildEnvHidesTheOuterTerminal(t *testing.T) {
	t.Parallel()
	outer := []string{
		"HOME=/tmp/h", "TERM=xterm-kitty", "TERM_PROGRAM=iTerm.app", "TERM_PROGRAM_VERSION=3.7.9",
		"LC_TERMINAL=iTerm2", "LC_TERMINAL_VERSION=3.7.9", "KITTY_WINDOW_ID=3", "TMUX=/tmp/tmux,1,0",
		"TMUX_PANE=%1", "COLUMNS=300", "LINES=90", "LANG=en_US.UTF-8",
	}
	got := childEnv(outer, 120, 40)
	want := []string{"HOME=/tmp/h", "LANG=en_US.UTF-8", "COLUMNS=120", "LINES=40", "TERM=xterm-256color"}
	if !slices.Equal(got, want) {
		t.Errorf("childEnv = %q, want %q", got, want)
	}
}

// A Kitty placeholder is one cell in the plain snapshot: blank, with its
// diacritics on it, so the text after it stays in its column.
func TestSnapshotToPNGKeepsColumnsAfterPlaceholders(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	read := func(name string, rows ...string) []byte {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := snapshotToPNG(emulator.EmittedFrame{Rows: rows}, path); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	marks := string([]rune{0x0305, 0x030D})
	got := read("placeholders.png", "\U0010EEEE"+marks+"\U0010EEEE"+marks+"x")
	want := read("blanks.png", "  x")
	if !bytes.Equal(got, want) {
		t.Fatal("the plain snapshot of placeholders and x differs from two blanks and x")
	}
}

func TestParseKeyTokenHex(t *testing.T) {
	tests := []struct {
		token   string
		want    string
		wantErr bool
	}{
		{token: "0x41", want: "A"},
		{token: "0x1f600", want: "\U0001F600"},
		{token: "0x110000", wantErr: true},    // past the last code point
		{token: "0xd800", wantErr: true},      // surrogate
		{token: "0x100000041", wantErr: true}, // would wrap to "A" as a rune
	}
	for _, tt := range tests {
		t.Run(tt.token, func(t *testing.T) {
			got, err := parseKeyToken(tt.token)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}
