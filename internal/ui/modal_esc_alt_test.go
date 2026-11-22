package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

type keyRecorder struct{ keys []string }

func (r *keyRecorder) Init() tea.Cmd { return nil }
func (r *keyRecorder) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		r.keys = append(r.keys, k.String())
	}
	return r, nil
}
func (r *keyRecorder) View() tea.View { return tea.NewView("") }

// Esc <char> in a viewer is the fallback for M-<char>: Esc a copies the
// output, Esc c the command. The viewer must get the Meta key and stay open
// when the Esc timeout fires afterwards.
func TestModalEscCharIsMetaKey(t *testing.T) {
	cases := []struct {
		name string
		key  tea.KeyPressMsg
		want string
	}{
		{"lower", tea.KeyPressMsg{Code: 'a', Text: "a"}, "alt+a"},
		{"command", tea.KeyPressMsg{Code: 'c', Text: "c"}, "alt+c"},
		{"upper", tea.KeyPressMsg{Code: 'A', Text: "A", Mod: tea.ModShift}, "alt+A"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &keyRecorder{}
			closed := false
			m := NewModal("viewer", rec)
			m.SetCloseOnSingleEsc(false)
			m.SetCloseOnEscTimeout(true)
			m.SetOnClose(func() tea.Cmd { closed = true; return nil })
			m.Show()

			m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
			m.Update(tc.key)
			m.Update(EscTimeoutMsg{})

			if len(rec.keys) != 1 || rec.keys[0] != tc.want {
				t.Fatalf("content got %q, want [%q]", rec.keys, tc.want)
			}
			if !m.IsVisible() || closed {
				t.Fatalf("Esc %s must not close the viewer", tc.key.String())
			}
		})
	}

	t.Run("plain key without Esc", func(t *testing.T) {
		rec := &keyRecorder{}
		m := NewModal("viewer", rec)
		m.Show()
		m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
		if len(rec.keys) != 1 || rec.keys[0] != "a" {
			t.Fatalf("content got %q, want [\"a\"]", rec.keys)
		}
	})

	t.Run("ctrl key after Esc is not rewritten", func(t *testing.T) {
		rec := &keyRecorder{}
		m := NewModal("viewer", rec)
		m.SetCloseOnSingleEsc(false)
		m.Show()
		m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
		m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
		if len(rec.keys) != 1 || rec.keys[0] != "ctrl+s" {
			t.Fatalf("content got %q, want [\"ctrl+s\"]", rec.keys)
		}
	})
}

// The real viewer answers Esc a with a copy of its output.
func TestTextViewerEscACopies(t *testing.T) {
	v := NewTextViewer("t", "hello", "", "", "", "", nil, nil, nil)
	v.SetCommand("kubectl get x")
	m := NewModal("viewer", v)
	m.SetCloseOnSingleEsc(false)
	m.SetCloseOnEscTimeout(true)
	m.SetDimensions(80, 24)
	m.Show()
	m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if cmd == nil {
		t.Fatalf("Esc a must start a copy")
	}
	m.Update(EscTimeoutMsg{})
	if !m.IsVisible() {
		t.Fatalf("Esc a must not close the viewer")
	}
}
