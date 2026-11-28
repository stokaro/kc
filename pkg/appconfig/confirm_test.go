package appconfig_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/stokaro/kc/pkg/appconfig"
)

// confirm.quit asks while sessions run by default; "always" asks every
// time; a value kc does not know keeps the default and is reported, so a
// misspelled "always" is not taken for "sessions" in silence.
func TestConfirmQuit(t *testing.T) {
	t.Parallel()
	if got := appconfig.Default().Confirm.Quit; got != appconfig.QuitConfirmSessions {
		t.Fatalf("default confirm.quit = %q, want sessions", got)
	}
	tests := []struct {
		name       string
		file       string
		want       appconfig.QuitConfirm
		wantReason string
	}{
		{name: "always", file: "confirm:\n  quit: always\n", want: appconfig.QuitConfirmAlways},
		{name: "case and spaces", file: "confirm:\n  quit: ' Always '\n", want: appconfig.QuitConfirmAlways},
		{name: "empty is the default", file: "confirm:\n  quit: ''\n", want: appconfig.QuitConfirmSessions},
		{name: "no section", file: "viewer:\n  theme: monokai\n", want: appconfig.QuitConfirmSessions},
		{
			name: "unknown value", file: "confirm:\n  quit: allways\n", want: appconfig.QuitConfirmSessions,
			wantReason: `confirm.quit: unknown value "allways"; use sessions or always`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := appconfig.NewStore(writeConfig(t, tc.file, 0o600)).Load()
			if tc.wantReason == "" {
				if err != nil {
					t.Fatalf("Load: %v", err)
				}
			} else {
				le, ok := errors.AsType[*appconfig.LoadError](err)
				if !ok {
					t.Fatalf("Load error = %v, want a *LoadError", err)
				}
				if !slices.Equal(le.Sections, []string{"confirm"}) || le.Reason() != tc.wantReason {
					t.Fatalf("sections %q, reason %q; want confirm, %q", le.Sections, le.Reason(), tc.wantReason)
				}
			}
			if cfg.Confirm.Quit != tc.want {
				t.Fatalf("confirm.quit = %q, want %q", cfg.Confirm.Quit, tc.want)
			}
		})
	}
}
