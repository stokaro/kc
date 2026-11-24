package main

import (
	"bytes"
	"encoding/base64"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/stokaro/kc/internal/ui/kittygfx"
)

// filterAll reads all of input through an outputFilter, one byte per read
// of the source when oneByte is set, so every sequence is cut at every
// byte.
func filterAll(t *testing.T, input string, oneByte bool) (out, reply string, g *graphics) {
	t.Helper()
	var src io.Reader = strings.NewReader(input)
	if oneByte {
		src = iotest.OneByteReader(src)
	}
	var replies bytes.Buffer
	g = newGraphics(9, 18)
	f := newOutputFilter(src, &replies, g, func() (int, int) { return 150, 42 })
	got, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(got), replies.String(), g
}

func TestOutputFilterTakesGraphicsOut(t *testing.T) {
	t.Parallel()
	enc := kittygfx.Encoder{}
	transmit := enc.Transmit(240, encodePNG(t, noise(40, 30)), 4, 2)
	input := "ab" + transmit + "\x1b[38;5;240m" + kittygfx.Cells(0, 0, 2) + "\x1b[39m" + enc.Delete(99) + "cd"
	want := "ab\x1b[38;5;240m" + kittygfx.Cells(0, 0, 2) + "\x1b[39mcd"
	for _, oneByte := range []bool{false, true} {
		out, reply, g := filterAll(t, input, oneByte)
		if out != want {
			t.Errorf("one byte %v: output %q, want %q", oneByte, out, want)
		}
		if reply != "" {
			t.Errorf("one byte %v: reply %q, want none", oneByte, reply)
		}
		if img := g.images[240]; img == nil || img.cols != 4 || img.rows != 2 {
			t.Errorf("one byte %v: image 240 = %+v, want a 4x2 placement", oneByte, img)
		}
	}
}

func TestOutputFilterPassesOtherSequences(t *testing.T) {
	t.Parallel()
	inputs := []string{
		"plain text\r\n",
		"\x1b[?25l\x1b[2J\x1b[1;1H\x1b[0m",
		"\x1b]0;title\x07\x1b]8;;https://example.com\x1b\\link\x1b]8;;\x1b\\",
		"\x1b_Xnot graphics\x1b\\",
		"\x1bPq#0;2;0;0;0\x1b\\",
		"\x1b\x1b[A",
		"\x1b7\x1b8\x1b=",
		"\x1b[18t\x1b[22;0t",
		"\x1b[1;2\x08m",
		"tail with a lone ESC\x1b",
	}
	for _, input := range inputs {
		for _, oneByte := range []bool{false, true} {
			out, reply, _ := filterAll(t, input, oneByte)
			// An ESC is held until the byte after it arrives.
			want, _ := strings.CutSuffix(input, "\x1b")
			if out != want {
				t.Errorf("%q (one byte %v): output %q", input, oneByte, out)
			}
			if reply != "" {
				t.Errorf("%q: reply %q, want none", input, reply)
			}
		}
	}
}

func TestOutputFilterAnswersSizeQueries(t *testing.T) {
	t.Parallel()
	tests := []struct {
		query, reply string
	}{
		{query: "\x1b[16t", reply: "\x1b[6;18;9t"},
		{query: "\x1b[14t", reply: "\x1b[4;756;1350t"},
		{query: "x\x1b[16ty\x1b[14t", reply: "\x1b[6;18;9t\x1b[4;756;1350t"},
	}
	for _, tt := range tests {
		for _, oneByte := range []bool{false, true} {
			out, reply, _ := filterAll(t, tt.query, oneByte)
			if reply != tt.reply {
				t.Errorf("%q (one byte %v): reply %q, want %q", tt.query, oneByte, reply, tt.reply)
			}
			if out != tt.query {
				t.Errorf("%q: output %q; the query passes through", tt.query, out)
			}
		}
	}
}

// An ESC that does not end a graphics command cancels it and starts the
// next sequence.
func TestOutputFilterEscCancelsCommand(t *testing.T) {
	t.Parallel()
	png64 := base64.StdEncoding.EncodeToString(encodePNG(t, quadrants(4, 4)))
	out, _, g := filterAll(t, "\x1b_Ga=T,U=1,f=100,i=5;"+png64+"\x1b[31mx", true)
	if out != "\x1b[31mx" {
		t.Errorf("output %q", out)
	}
	if len(g.images) != 0 {
		t.Error("the canceled command stored an image")
	}
}

// The filter returns the bytes it read with an error before the error.
func TestOutputFilterReturnsDataBeforeError(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	src := io.MultiReader(strings.NewReader("ab\x1b_Ga=d,d=I,i=1\x1b\\cd"), iotest.ErrReader(boom))
	f := newOutputFilter(iotest.DataErrReader(src), io.Discard, newGraphics(9, 18), nil)
	got, err := io.ReadAll(f)
	if string(got) != "abcd" || !errors.Is(err, boom) {
		t.Fatalf("got %q, %v; want \"abcd\", boom", got, err)
	}
}
