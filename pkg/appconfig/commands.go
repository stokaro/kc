package appconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/sttts/kc/internal/cmdtemplate"
	yaml "sigs.k8s.io/yaml"
)

// CommandPrompt is one named input of a custom command. The dialog shows
// Label and starts with Default; with Choices the answer is one of them.
type CommandPrompt struct {
	Name    string   `json:"name"`
	Label   string   `json:"label,omitempty"`
	Default string   `json:"default,omitempty"`
	Choices []string `json:"choices,omitempty"`
}

// CommandConfirm is the `confirm:` setting of a custom command. In YAML it
// is `true` (a default title) or a message, which is a command template
// itself.
type CommandConfirm struct {
	Enabled bool
	Message string
}

// IsZero reports an unset confirmation, so `omitzero` leaves it out.
func (c CommandConfirm) IsZero() bool { return !c.Enabled && c.Message == "" }

// MarshalJSON writes true, false or the message.
func (c CommandConfirm) MarshalJSON() ([]byte, error) {
	if c.Enabled && c.Message != "" {
		return json.Marshal(c.Message)
	}
	return json.Marshal(c.Enabled)
}

// UnmarshalJSON accepts a boolean, a string (an empty one means no
// confirmation) or null.
func (c *CommandConfirm) UnmarshalJSON(data []byte) error {
	var b bool
	if err := json.Unmarshal(data, &b); err == nil {
		*c = CommandConfirm{Enabled: b}
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("confirm: want true, false or a message, got %s", data)
	}
	s = strings.TrimSpace(s)
	*c = CommandConfirm{Enabled: s != "", Message: s}
	return nil
}

// unmarshalOverDefaults decodes a configuration file onto cfg, which holds
// the defaults. encoding/json decodes a list into the existing elements, so
// the user's first command would inherit the fields of the first default
// command (watchInterval, interactive, onExit, ...) that it does not set.
// The default commands are therefore kept only when the file has no
// commands; `commands: []` means none.
func unmarshalOverDefaults(data []byte, cfg *Config) error {
	defaults := cfg.Commands
	cfg.Commands = nil
	err := yaml.Unmarshal(data, cfg)
	if cfg.Commands == nil {
		cfg.Commands = defaults
	}
	return err
}

// MaxCommandPrompts bounds the prompts of one command, so the dialog fits
// on a small terminal.
const MaxCommandPrompts = 8

// commandChordPrefix is the only prefix key of two-key hotkeys, as in mc.
const commandChordPrefix = "C-x"

// builtinHotkeys lists the keys kc binds itself in the panels, in the
// notation ParseHotkey returns. Each M-<letter> also has an Esc <letter>
// fallback, so a user key must not take the letter either.
var builtinHotkeys = map[string]string{
	"M-a":     "copy all in the viewer",
	"M-A":     "copy original log records",
	"M-b":     "used-by objects",
	"M-c":     "copy command",
	"M-e":     "explain fields",
	"M-f":     "row filter",
	"M-g":     "log filter",
	"M-H":     "history list",
	"M-i":     "open the overview",
	"M-j":     "owners",
	"M-k":     "access view",
	"M-l":     "local directory panel",
	"M-n":     "node actions",
	"M-o":     "sort by column",
	"M-p":     "port forward",
	"M-r":     "reverse sort",
	"M-s":     "quick search",
	"M-t":     "attach to a container",
	"M-u":     "history forward",
	"M-v":     "raw or formatted logs",
	"M-w":     "cycle panel width",
	"M-x":     "shell in a container",
	"M-y":     "history back",
	"C-x C-d": "compare manifests",
}

// BuiltinHotkey reports whether kc binds key (normalized by ParseHotkey)
// itself and what for.
func BuiltinHotkey(key string) (string, bool) {
	what, ok := builtinHotkeys[key]
	return what, ok
}

// ParseHotkey checks a hotkey in mc notation and returns it normalized:
// "M-<char>" or "C-M-<char>" (Meta/Alt, the character is case-sensitive),
// "F11" to "F24", or a two-key chord "C-x <char>" or "C-x C-<letter>".
// Keys without a modifier would take plain typing, single C-<letter> keys
// belong to kc and to the command line, and F1 to F10 are kc's function
// keys, so those are refused.
func ParseHotkey(s string) (string, error) {
	fields := strings.Fields(s)
	switch len(fields) {
	case 0:
		return "", errors.New("empty hotkey")
	case 1:
		st, err := parseStroke(fields[0])
		if err != nil {
			return "", err
		}
		switch {
		case st.fkey > 0 && st.fkey <= 10:
			return "", fmt.Errorf("%s is a kc function key; use F11 to F24", st)
		case st.fkey > 0:
		case st.meta:
		case st.ctrl:
			return "", fmt.Errorf("%s belongs to kc or to the command line; use M-<key> or a %s <key> chord", st, commandChordPrefix)
		default:
			return "", fmt.Errorf("%s has no modifier and would take plain typing; use M-%s", st, st)
		}
		return st.String(), nil
	case 2:
		first, err := parseStroke(fields[0])
		if err != nil {
			return "", err
		}
		if first.String() != commandChordPrefix {
			return "", fmt.Errorf("a two-key hotkey starts with %s, not %s", commandChordPrefix, first)
		}
		second, err := parseStroke(fields[1])
		if err != nil {
			return "", err
		}
		if second.meta || second.fkey > 0 {
			return "", fmt.Errorf("after %s use a key or C-<letter>, not %s", commandChordPrefix, second)
		}
		return first.String() + " " + second.String(), nil
	default:
		return "", fmt.Errorf("hotkey %q has more than two keys", s)
	}
}

type stroke struct {
	ctrl, meta bool
	fkey       int
	char       rune
}

func (s stroke) String() string {
	var b strings.Builder
	if s.ctrl {
		b.WriteString("C-")
	}
	if s.meta {
		b.WriteString("M-")
	}
	if s.fkey > 0 {
		b.WriteString("F" + strconv.Itoa(s.fkey))
	} else {
		b.WriteRune(s.char)
	}
	return b.String()
}

func parseStroke(s string) (stroke, error) {
	var st stroke
	rest := s
	for {
		switch {
		case len(rest) > 2 && strings.HasPrefix(rest, "C-") && !st.ctrl:
			st.ctrl = true
			rest = rest[2:]
			continue
		case len(rest) > 2 && strings.HasPrefix(rest, "M-") && !st.meta:
			st.meta = true
			rest = rest[2:]
			continue
		}
		break
	}
	if n, ok := functionKey(rest); ok {
		if st.ctrl || st.meta {
			return st, fmt.Errorf("key %q: modifiers on function keys are not supported", s)
		}
		st.fkey = n
		return st, nil
	}
	r, size := utf8.DecodeRuneInString(rest)
	if size != len(rest) || r == utf8.RuneError || !unicode.IsPrint(r) || unicode.IsSpace(r) {
		return st, fmt.Errorf("key %q: want one character, F<n>, or C-/M- followed by one", s)
	}
	if st.ctrl {
		if !unicode.IsLetter(r) || r > unicode.MaxASCII {
			return st, fmt.Errorf("key %q: C- takes a letter", s)
		}
		r = unicode.ToLower(r)
	}
	st.char = r
	return st, nil
}

func functionKey(s string) (int, bool) {
	if len(s) < 2 || (s[0] != 'F' && s[0] != 'f') {
		return 0, false
	}
	n, err := strconv.Atoi(s[1:])
	if err != nil || n < 1 || n > 24 {
		return 0, false
	}
	return n, true
}

var promptName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidateCommands checks the hotkeys, prompts and confirmations of cmds
// and returns the hotkey table: the normalized hotkey (see ParseHotkey)
// mapped to the index of its command. It does not change cmds, so a
// configuration saved later keeps what the user wrote. A bad, duplicate or
// built-in hotkey is left out of the table. A command that ValidateCommand
// refuses gets no hotkey either. The error lists every problem.
func ValidateCommands(cmds []CommandConfig) (map[string]int, error) {
	var errs []error
	keys := map[string]int{}
	for i, cmd := range cmds {
		where := fmt.Sprintf("commands[%d] %q", i, cmd.Name)
		if err := ValidateCommand(cmd); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w; the command does not run", where, err))
			continue
		}
		if cmd.Hotkey == "" {
			continue
		}
		key, err := ParseHotkey(cmd.Hotkey)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: hotkey %q: %w; hotkey ignored", where, cmd.Hotkey, err))
			continue
		}
		if what, ok := builtinHotkeys[key]; ok {
			errs = append(errs, fmt.Errorf("%s: hotkey %s is kc's key for %s; hotkey ignored", where, key, what))
			continue
		}
		if j, ok := keys[key]; ok {
			errs = append(errs, fmt.Errorf("%s: hotkey %s is already bound to %q; hotkey ignored", where, key, cmds[j].Name))
			continue
		}
		keys[key] = i
	}
	return keys, errors.Join(errs...)
}

// ValidateCommand checks what a command needs to run as configured: its
// prompts, the prompt names its templates read, and that kc can honor its
// confirmation. kc refuses to run a command that fails, since running it
// would break on the template or skip the question.
func ValidateCommand(cmd CommandConfig) error {
	if len(cmd.Prompts) > MaxCommandPrompts {
		return fmt.Errorf("%d prompts, at most %d are supported", len(cmd.Prompts), MaxCommandPrompts)
	}
	var names []string
	for j, p := range cmd.Prompts {
		switch {
		case !promptName.MatchString(p.Name):
			return fmt.Errorf("prompts[%d]: name %q must be a template identifier (letters, digits, _)", j, p.Name)
		case slices.Contains(names, p.Name):
			return fmt.Errorf("prompts[%d]: name %q is used twice", j, p.Name)
		case len(p.Choices) > 0 && p.Default != "" && !slices.Contains(p.Choices, p.Default):
			return fmt.Errorf("prompts[%d] %q: default %q is not one of the choices", j, p.Name, p.Default)
		}
		names = append(names, p.Name)
	}
	for _, tmpl := range []struct{ field, text string }{{"command", cmd.Command}, {"confirm", cmd.Confirm.Message}} {
		refs, err := cmdtemplate.InputRefs(tmpl.text)
		if err != nil {
			return fmt.Errorf("%s: %w", tmpl.field, err)
		}
		for _, ref := range refs {
			if !slices.Contains(names, ref) {
				return fmt.Errorf("%s reads .%s.%s but there is no prompt %q", tmpl.field, cmdtemplate.InputField, ref, ref)
			}
		}
	}
	if cmd.Confirm.Enabled {
		// kc reruns these on its own; a confirmation approves one run.
		if cmd.Type == CommandTypeNamespace {
			return errors.New("confirm does not work with type namespace, which reruns on every namespace change")
		}
		if cmd.WatchInterval.Duration > 0 {
			return errors.New("confirm does not work with watchInterval, which reruns the command")
		}
	}
	return nil
}
