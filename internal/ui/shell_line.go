package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// shellLine follows what the user types on the shell line, so kc can tell
// what Enter would run. The shell owns the real line editor; kc only knows
// the text while every key was a plain character, Backspace, C-u or, on an
// empty line, a key that adds no text (leavesLineEmpty). After any other
// key (Tab completion, cursor moves, history, word edits) the text is
// unknown until the next Enter or C-c starts a fresh line.
type shellLine struct {
	text    []rune
	unknown bool
}

// key records a key sent to the shell.
func (l *shellLine) key(k tea.KeyPressMsg) {
	switch k.String() {
	case "enter", "ctrl+c":
		l.reset()
		return
	case "backspace":
		if len(l.text) > 0 {
			l.text = l.text[:len(l.text)-1]
		}
		return
	case "ctrl+u":
		l.text = l.text[:0]
		return
	}
	if len(l.text) == 0 && leavesLineEmpty(k) {
		return
	}
	key := k.Key()
	if key.Text != "" && key.Mod&^tea.ModShift == 0 {
		l.text = append(l.text, []rune(key.Text)...)
		return
	}
	l.unknown = true
}

// leavesLineEmpty reports keys that put no text on an empty line in bash
// or zsh with no configuration: cursor moves, kill to end, clear screen,
// EOF and abort. Delete and the modified arrows are not here: without a
// binding zsh types a "~" for Delete, and both shells type ";5C" for C-Right.
func leavesLineEmpty(k tea.KeyPressMsg) bool {
	switch k.String() {
	case "ctrl+a", "ctrl+b", "ctrl+d", "ctrl+e", "ctrl+f", "ctrl+g", "ctrl+k", "ctrl+l":
		return true
	}
	return false
}

// paste records pasted text; a multi-line paste leaves the line unknown.
func (l *shellLine) paste(s string) {
	if strings.ContainsAny(s, "\r\n") {
		l.unknown = true
		return
	}
	l.text = append(l.text, []rune(s)...)
}

// invalidate marks the line unknown until the next fresh line.
func (l *shellLine) invalidate() { l.unknown = true }

// reset starts a fresh, empty line.
func (l *shellLine) reset() {
	l.text = l.text[:0]
	l.unknown = false
}

// current returns the line and whether kc knows it.
func (l *shellLine) current() (string, bool) {
	if l.unknown {
		return "", false
	}
	return string(l.text), true
}
