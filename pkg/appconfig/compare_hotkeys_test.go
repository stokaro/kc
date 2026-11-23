package appconfig_test

import (
	"testing"

	"github.com/stokaro/kc/pkg/appconfig"
)

// The list comparison chords are kc's own: a custom command cannot take
// them. The keys are checked in the form ParseHotkey returns.
func TestCompareChordsAreBuiltin(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"C-x d", "C-x c"} {
		key, err := appconfig.ParseHotkey(in)
		if err != nil {
			t.Fatalf("ParseHotkey(%q): %v", in, err)
		}
		if _, ok := appconfig.BuiltinHotkey(key); !ok {
			t.Errorf("%s is not reserved", key)
		}
	}
	// The control: a free chord stays free.
	if _, ok := appconfig.BuiltinHotkey("C-x 2"); ok {
		t.Error("C-x 2 is reserved")
	}
}
