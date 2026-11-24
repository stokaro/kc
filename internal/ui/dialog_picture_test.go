package ui

import (
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/stokaro/kc/internal/overlay"
	"github.com/stokaro/kc/internal/ui/kittygfx"
)

// A dialog over a picture: the cells it covers go, the cells beside it
// keep their image and their own row and column, and its shadow drops the
// placeholders, whose new color would name another image.
func TestDialogOverAPicture(t *testing.T) {
	t.Parallel()
	const id = 0xC2
	row := func(r int) string {
		return "ab" + kittygfx.Color(id) + kittygfx.Cells(r, 0, 10) + "\x1b[39m" + "cd"
	}
	base := strings.Join([]string{row(0), row(1), row(2), row(3)}, "\n")
	win := strings.Join([]string{"WWWW", "WWWW"}, "\n")
	screen := castShadow(overlay.Composite(win, base, overlay.Left, overlay.Top, 4, 0), base, 4, 0, 4, 2)
	for y, l := range strings.Split(screen, "\n") {
		buf := uv.NewScreenBuffer(14, 1)
		uv.NewStyledString(l).Draw(buf, buf.Bounds())
		for x := range 14 {
			c := buf.CellAt(x, 0)
			isPic := strings.HasPrefix(c.Content, string(kittygfx.Placeholder))
			covered := y < 2 && x >= 4 && x < 8
			shadow := (y == 1 && x >= 8 && x < 10) || (y == 2 && x >= 6 && x < 10)
			wantPic := x >= 2 && x < 12 && !covered && !shadow
			if isPic != wantPic {
				t.Fatalf("cell %d,%d %q: picture %v, want %v", x, y, c.Content, isPic, wantPic)
			}
			if !isPic {
				continue
			}
			rd, _ := kittygfx.Diacritic(y)
			cd, _ := kittygfx.Diacritic(x - 2)
			if c.Content != string([]rune{kittygfx.Placeholder, rd, cd}) || c.Style.Fg != ansi.IndexedColor(id) {
				t.Errorf("cell %d,%d is %q in %v", x, y, c.Content, c.Style.Fg)
			}
		}
	}
}
