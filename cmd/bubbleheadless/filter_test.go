package main

import (
	"bytes"
	"encoding/base64"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
	"testing/synctest"
	"time"
	"unicode/utf8"

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

// chunkReader returns its input size bytes per read.
type chunkReader struct {
	s    string
	size int
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if r.s == "" {
		return 0, io.EOF
	}
	n := copy(p[:min(len(p), r.size)], r.s)
	r.s = r.s[n:]
	return n, nil
}

// Every read of the filter ends between grapheme clusters, however the
// source and the reader's buffer cut the output: the emulator ends a
// cluster at the end of each write.
func TestOutputFilterReturnsWholeClusters(t *testing.T) {
	t.Parallel()
	rows := kittygfx.Rows(150, 3)
	input := "\x1b[1;1H" + kittygfx.Color(240) + rows[0] + "\x1b[39m│x" +
		kittygfx.Color(240) + rows[1] + "e\u0301\U0001F44D\U0001F3FD\U0001F469\u200D\U0001F4BB!" + rows[2]
	diacritic := make(map[rune]bool, kittygfx.MaxCells)
	for n := range kittygfx.MaxCells {
		r, _ := kittygfx.Diacritic(n)
		diacritic[r] = true
	}
	// joins reports whether a cut between prev and r splits what the
	// emulator takes as one cluster. The emulator draws an ASCII rune at
	// once, so a mark after one is lost in any case; the filter holds no
	// ASCII.
	joins := func(prev, r rune) bool {
		return prev == '\u200D' || prev >= utf8.RuneSelf && (diacritic[r] || r == '\u0301' || r == '\U0001F3FD' || r == '\u200D')
	}
	// Buffers of many sizes put the cut at every place in a cluster.
	bufSizes := []int{4096}
	for n := 61; n < 80; n++ {
		bufSizes = append(bufSizes, n)
	}
	for _, srcSize := range []int{1, 2, 3, 5, 7, 4096} {
		for _, bufSize := range bufSizes {
			f := newOutputFilter(&chunkReader{s: input, size: srcSize}, io.Discard, newGraphics(9, 18), nil)
			f.holdFor = time.Minute // only the end of the input releases a cluster
			var got strings.Builder
			buf := make([]byte, bufSize)
			for {
				n, err := f.Read(buf)
				chunk := buf[:n]
				if n > 0 && got.Len() > 0 {
					prev, _ := utf8.DecodeLastRuneInString(got.String())
					if r, _ := utf8.DecodeRune(chunk); !utf8.RuneStart(chunk[0]) || joins(prev, r) {
						t.Fatalf("source %d, buffer %d: a read starts inside a cluster: %q after %q",
							srcSize, bufSize, chunk[:min(n, 8)], got.String()[max(0, got.Len()-12):])
					}
				}
				got.Write(chunk)
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if got.String() != input {
				t.Fatalf("source %d, buffer %d: output differs from the input", srcSize, bufSize)
			}
		}
	}
}

// A held cluster goes out when the next output starts a new one, when
// the output pauses for holdFor, or when the source ends.
func TestOutputFilterReleasesHeldCluster(t *testing.T) {
	t.Parallel()
	cell := kittygfx.Cells(4, 7, 8)
	marks := cell[utf8.RuneLen(kittygfx.Placeholder):]
	synctest.Test(t, func(t *testing.T) {
		pr, pw := io.Pipe()
		f := newOutputFilter(pr, io.Discard, newGraphics(9, 18), nil)
		t.Cleanup(func() {
			_ = pw.Close()
			_ = f.Close()
		})
		buf := make([]byte, 64)
		read := func() string {
			t.Helper()
			n, err := f.Read(buf)
			if err != nil {
				t.Fatal(err)
			}
			return string(buf[:n])
		}

		// The diacritics after a pause shorter than holdFor join their
		// placeholder.
		go func() {
			_, _ = io.WriteString(pw, "ab"+string(kittygfx.Placeholder))
			time.Sleep(holdFor / 2)
			_, _ = io.WriteString(pw, marks+cell)
		}()
		if got := read(); got != "ab" {
			t.Fatalf("first read %q, want \"ab\"", got)
		}
		if got := read(); got != cell {
			t.Fatalf("second read %q, want the whole first cell", got)
		}

		// The last cell waits holdFor for more diacritics.
		start := time.Now()
		if got := read(); got != cell {
			t.Fatalf("third read %q, want the second cell", got)
		}
		if waited := time.Since(start); waited != holdFor {
			t.Errorf("the last cell waited %v, want %v", waited, holdFor)
		}

		// An escape sequence ends the cluster before it: that cell goes
		// out at once, while the filter holds the ESC.
		go func() { _, _ = io.WriteString(pw, cell+"\x1b") }()
		start = time.Now()
		if got := read(); got != cell {
			t.Fatalf("read %q, want the cell before the ESC", got)
		}
		if waited := time.Since(start); waited != 0 {
			t.Errorf("the cell before an ESC waited %v", waited)
		}
		go func() { _, _ = io.WriteString(pw, "[0m") }()
		if got := read(); got != "\x1b[0m" {
			t.Fatalf("read %q, want the sequence", got)
		}
	})
}

// extendsCluster covers kitty's diacritics, so no cut separates a
// placeholder from them.
func TestExtendsClusterCoversKittyDiacritics(t *testing.T) {
	t.Parallel()
	for n := range kittygfx.MaxCells {
		r, _ := kittygfx.Diacritic(n)
		if !extendsCluster(r) {
			t.Errorf("diacritic %d (%U) does not extend a cluster", n, r)
		}
	}
	for _, r := range []rune{'a', kittygfx.Placeholder, '│', '\U0001F44D'} {
		if extendsCluster(r) {
			t.Errorf("%U extends a cluster", r)
		}
	}
}
