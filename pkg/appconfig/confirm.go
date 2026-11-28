package appconfig

import (
	"fmt"
	"strings"
)

// QuitConfirm is when quitting kc asks first (confirm.quit).
type QuitConfirm string

const (
	// QuitConfirmSessions asks while something would stop with kc: port
	// forwards, a node shell, a copy. Quitting with nothing running takes
	// one key.
	QuitConfirmSessions QuitConfirm = "sessions"
	// QuitConfirmAlways asks every time.
	QuitConfirmAlways QuitConfirm = "always"
)

// ConfirmConfig decides when kc asks before a step that ends something
// the user started.
type ConfirmConfig struct {
	// Quit is when F10 (Esc 0, File > Exit) asks before kc exits.
	Quit QuitConfirm `json:"quit"`
}

// normalize lower-cases the value and fills an empty one with the default,
// so `quit: Always` and `quit: always` mean the same.
func (c *ConfirmConfig) normalize() {
	c.Quit = QuitConfirm(strings.ToLower(strings.TrimSpace(string(c.Quit))))
	if c.Quit == "" {
		c.Quit = QuitConfirmSessions
	}
}

// Validate reports a value kc does not know. A misspelled "always" must
// not quietly become "sessions": the user asked for more questions.
func (c ConfirmConfig) Validate() error {
	switch c.Quit {
	case QuitConfirmSessions, QuitConfirmAlways:
		return nil
	}
	return fmt.Errorf("confirm.quit: unknown value %q; use sessions or always", string(c.Quit))
}
