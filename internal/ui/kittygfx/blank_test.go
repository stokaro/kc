package kittygfx_test

import (
	"testing"

	"github.com/stokaro/kc/internal/ui/kittygfx"
)

func TestBlank(t *testing.T) {
	t.Parallel()
	d, _ := kittygfx.Diacritic(3)
	for _, tc := range []struct{ name, in, want string }{
		{"no placeholders", "CPU 5%", "CPU 5%"},
		{"cells", "ab" + kittygfx.Cells(1, 2, 5) + "cd", "ab   cd"},
		{"a diacritic after text stays", "e" + string(d), "e" + string(d)},
		{"bare placeholder", "x" + string(kittygfx.Placeholder) + "y", "x y"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := kittygfx.Blank(tc.in); got != tc.want {
				t.Errorf("Blank = %q, want %q", got, tc.want)
			}
		})
	}
}
