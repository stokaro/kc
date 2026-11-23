package dashboard

import tea "charm.land/bubbletea/v2"

// TickMsgForTest is the tick message of m's current generation.
func TickMsgForTest(m *Model) TickMsg { return TickMsg{id: m.id} }

// SettledMsgForTest is the message that ends the settling of m's last
// resize.
func SettledMsgForTest(m *Model) tea.Msg { return settledMsg{id: m.id, seq: m.resizes} }
