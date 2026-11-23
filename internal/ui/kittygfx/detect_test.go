package kittygfx_test

import (
	"testing"

	"github.com/stokaro/kc/internal/ui/kittygfx"
)

func TestDetect(t *testing.T) {
	t.Parallel()
	type tmuxAnswer struct {
		client, pass string
		ok           bool
	}
	cases := []struct {
		name     string
		mode     kittygfx.Mode
		env      map[string]string
		tmux     *tmuxAnswer
		on, wrap bool
	}{
		{name: "text never draws images", mode: kittygfx.ModeText, env: map[string]string{"TERM": "xterm-kitty"}},
		{name: "kitty always draws them", mode: kittygfx.ModeKitty, env: map[string]string{"TERM": "xterm-256color"}, on: true},
		{name: "kitty mode under tmux wraps", mode: kittygfx.ModeKitty, env: map[string]string{"TMUX": "/tmp/t,1,0"}, on: true, wrap: true},
		{name: "unknown terminal", mode: kittygfx.ModeAuto, env: map[string]string{"TERM": "xterm-256color", "TERM_PROGRAM": "Apple_Terminal"}},
		{name: "iTerm2 3.7.9 by TERM_PROGRAM", mode: kittygfx.ModeAuto, env: map[string]string{"TERM_PROGRAM": "iTerm.app", "TERM_PROGRAM_VERSION": "3.7.9"}, on: true},
		{name: "iTerm2 by LC_TERMINAL", mode: kittygfx.ModeAuto, env: map[string]string{"LC_TERMINAL": "iTerm2", "LC_TERMINAL_VERSION": "3.8.0beta1"}, on: true},
		{name: "iTerm2 3.6 draws no placeholders", mode: kittygfx.ModeAuto, env: map[string]string{"TERM_PROGRAM": "iTerm.app", "TERM_PROGRAM_VERSION": "3.6.4"}},
		{name: "iTerm2 4.0", mode: kittygfx.ModeAuto, env: map[string]string{"LC_TERMINAL": "iTerm2", "LC_TERMINAL_VERSION": "4.0.1"}, on: true},
		{name: "iTerm2 without a version", mode: kittygfx.ModeAuto, env: map[string]string{"LC_TERMINAL": "iTerm2"}},
		{name: "kitty by TERM, as over SSH", mode: kittygfx.ModeAuto, env: map[string]string{"TERM": "xterm-kitty"}, on: true},
		{name: "kitty by window ID", mode: kittygfx.ModeAuto, env: map[string]string{"TERM": "xterm-256color", "KITTY_WINDOW_ID": "3"}, on: true},
		{name: "Ghostty by TERM", mode: kittygfx.ModeAuto, env: map[string]string{"TERM": "xterm-ghostty"}, on: true},
		{name: "Ghostty by TERM_PROGRAM", mode: kittygfx.ModeAuto, env: map[string]string{"TERM_PROGRAM": "ghostty"}, on: true},
		{name: "WezTerm", mode: kittygfx.ModeAuto, env: map[string]string{"TERM_PROGRAM": "WezTerm", "TERM": "xterm-256color"}},
		{
			name: "tmux in kitty with passthrough", mode: kittygfx.ModeAuto,
			env:  map[string]string{"TMUX": "/tmp/t,1,0", "TERM": "tmux-256color", "TERM_PROGRAM": "tmux"},
			tmux: &tmuxAnswer{client: "xterm-kitty", pass: "on", ok: true}, on: true, wrap: true,
		},
		{
			name: "tmux in Ghostty with passthrough all", mode: kittygfx.ModeAuto,
			env:  map[string]string{"TMUX": "/tmp/t,1,0"},
			tmux: &tmuxAnswer{client: "xterm-ghostty", pass: "all\n", ok: true}, on: true, wrap: true,
		},
		{
			name: "tmux without passthrough", mode: kittygfx.ModeAuto,
			env:  map[string]string{"TMUX": "/tmp/t,1,0"},
			tmux: &tmuxAnswer{client: "xterm-kitty", pass: "off", ok: true},
		},
		{
			name: "tmux in iTerm2 3.7 by LC_TERMINAL", mode: kittygfx.ModeAuto,
			env:  map[string]string{"TMUX": "/tmp/t,1,0", "LC_TERMINAL": "iTerm2", "LC_TERMINAL_VERSION": "3.7.9"},
			tmux: &tmuxAnswer{client: "xterm-256color", pass: "on", ok: true}, on: true, wrap: true,
		},
		{
			name: "tmux in old iTerm2", mode: kittygfx.ModeAuto,
			env:  map[string]string{"TMUX": "/tmp/t,1,0", "LC_TERMINAL": "iTerm2", "LC_TERMINAL_VERSION": "3.5.0"},
			tmux: &tmuxAnswer{client: "xterm-256color", pass: "on", ok: true},
		},
		{
			name: "TERM_PROGRAM=iTerm.app under tmux is stale", mode: kittygfx.ModeAuto,
			env:  map[string]string{"TMUX": "/tmp/t,1,0", "TERM_PROGRAM": "iTerm.app", "TERM_PROGRAM_VERSION": "3.7.9"},
			tmux: &tmuxAnswer{client: "xterm-256color", pass: "on", ok: true},
		},
		{
			name: "tmux that cannot be asked", mode: kittygfx.ModeAuto,
			env:  map[string]string{"TMUX": "/tmp/t,1,0", "TERM": "xterm-kitty"},
			tmux: &tmuxAnswer{},
		},
		{name: "tmux without a way to ask", mode: kittygfx.ModeAuto, env: map[string]string{"TMUX": "/tmp/t,1,0", "TERM": "xterm-kitty"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env := kittygfx.Env{Getenv: func(k string) string { return tc.env[k] }}
			if tc.tmux != nil {
				a := *tc.tmux
				env.Tmux = func() (string, string, bool) { return a.client, a.pass, a.ok }
			}
			got := kittygfx.Detect(tc.mode, env)
			if got.On != tc.on || got.Tmux != tc.wrap {
				t.Errorf("Detect = %+v, want on=%v tmux=%v", got, tc.on, tc.wrap)
			}
			if got.Reason == "" {
				t.Error("no reason given")
			}
		})
	}
}
