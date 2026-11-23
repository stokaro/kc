package kittygfx

import (
	"strconv"
	"strings"
)

// Mode is the charts.graphics setting.
type Mode string

const (
	// ModeAuto uses images when the terminal is known to show Unicode
	// placeholders.
	ModeAuto Mode = "auto"
	// ModeText never uses images.
	ModeText Mode = "text"
	// ModeKitty always uses images.
	ModeKitty Mode = "kitty"
)

// Modes lists the valid modes.
func Modes() []Mode { return []Mode{ModeAuto, ModeText, ModeKitty} }

// Env is what detection reads: the environment, and, under tmux, the
// answers of the tmux server. Tmux is nil outside tests and the App; it
// returns the client's terminal name and the allow-passthrough option,
// and ok false when tmux could not be asked.
type Env struct {
	Getenv func(string) string
	Tmux   func() (clientTerm, passthrough string, ok bool)
}

// Support is what detection found.
type Support struct {
	// On is true when images are used.
	On bool
	// Tmux is true when the commands need the tmux passthrough.
	Tmux bool
	// Reason says why, for the log.
	Reason string
}

// Detect decides whether to draw images for mode m. It never writes to
// the terminal: a query would have to wait for an answer that some
// terminals never send, and its reply could be read as keys. It goes by
// the names terminals set instead:
//
//   - iTerm2 3.7 or later: LC_TERMINAL=iTerm2 with LC_TERMINAL_VERSION
//     (kept over SSH, which forwards LC_*), or TERM_PROGRAM=iTerm.app with
//     TERM_PROGRAM_VERSION. Earlier versions answer the protocol's query
//     but draw no placeholders;
//   - kitty: TERM=xterm-kitty (kept over SSH) or KITTY_WINDOW_ID;
//   - Ghostty: TERM=xterm-ghostty or TERM_PROGRAM=ghostty.
//
// Under tmux ($TMUX) TERM and TERM_PROGRAM describe tmux. The terminal is
// then tmux's client_termname, or iTerm2 by LC_TERMINAL, and the commands
// pass through only with allow-passthrough on (or all).
func Detect(m Mode, env Env) Support {
	getenv := env.Getenv
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	inTmux := getenv("TMUX") != ""
	switch m {
	case ModeText:
		return Support{Reason: "charts.graphics is text"}
	case ModeKitty:
		return Support{On: true, Tmux: inTmux, Reason: "charts.graphics is kitty"}
	}
	iterm, itermKnown := iTerm2(getenv, inTmux)
	if !inTmux {
		if itermKnown {
			return iterm
		}
		if name, ok := knownTerminal(getenv("TERM"), getenv("TERM_PROGRAM"), getenv("KITTY_WINDOW_ID")); ok {
			return Support{On: true, Reason: name}
		}
		return Support{Reason: "terminal not known to show Kitty Unicode placeholders"}
	}
	if env.Tmux == nil {
		return Support{Reason: "tmux: cannot ask the server"}
	}
	client, pass, ok := env.Tmux()
	if !ok {
		return Support{Reason: "tmux: cannot ask the server"}
	}
	name, known := knownTerminal(client, "", "")
	if itermKnown {
		name, known = "iTerm2", iterm.On
		if !iterm.On {
			return iterm
		}
	}
	switch pass = strings.TrimSpace(pass); {
	case !known:
		return Support{Reason: "tmux client terminal " + client + " not known to show Kitty Unicode placeholders"}
	case pass != "on" && pass != "all":
		return Support{Reason: "tmux allow-passthrough is off"}
	}
	return Support{On: true, Tmux: true, Reason: name + " through tmux"}
}

// iTerm2 reports whether the environment names iTerm2 (known) and whether
// that version shows placeholders. Under tmux only LC_TERMINAL counts:
// TERM_PROGRAM names tmux there.
func iTerm2(getenv func(string) string, inTmux bool) (Support, bool) {
	version := ""
	switch {
	case getenv("LC_TERMINAL") == "iTerm2":
		version = getenv("LC_TERMINAL_VERSION")
	case !inTmux && getenv("TERM_PROGRAM") == "iTerm.app":
		version = getenv("TERM_PROGRAM_VERSION")
	default:
		return Support{}, false
	}
	if !atLeast(version, 3, 7) {
		return Support{Reason: "iTerm2 " + version + " is older than 3.7 and draws no Kitty Unicode placeholders"}, true
	}
	return Support{On: true, Reason: "iTerm2 " + version}, true
}

// atLeast reports whether version (major.minor[.patch][suffix]) is at
// least major.minor; a version that does not parse is not.
func atLeast(version string, major, minor int) bool {
	parts := strings.SplitN(strings.TrimSpace(version), ".", 3)
	if len(parts) < 2 {
		return false
	}
	num := func(s string) (int, bool) {
		end := 0
		for end < len(s) && s[end] >= '0' && s[end] <= '9' {
			end++
		}
		n, err := strconv.Atoi(s[:end])
		return n, err == nil
	}
	ma, ok1 := num(parts[0])
	mi, ok2 := num(parts[1])
	if !ok1 || !ok2 {
		return false
	}
	return ma > major || (ma == major && mi >= minor)
}

// knownTerminal names a terminal that shows Unicode placeholders.
func knownTerminal(term, program, kittyWindow string) (string, bool) {
	term = strings.TrimSpace(term)
	switch {
	case term == "xterm-kitty" || kittyWindow != "":
		return "kitty", true
	case term == "xterm-ghostty" || strings.EqualFold(program, "ghostty"):
		return "Ghostty", true
	}
	return "", false
}
