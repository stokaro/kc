package dashboard

// TickMsgForTest is the tick message of m's current generation.
func TickMsgForTest(m *Model) TickMsg { return TickMsg{id: m.id} }
