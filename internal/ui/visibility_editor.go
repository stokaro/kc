package ui

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/stokaro/kc/internal/models"
	styles "github.com/stokaro/kc/internal/ui/styles"
	"github.com/stokaro/kc/internal/visibility"
	"github.com/stokaro/kc/pkg/appconfig"
)

// visibilityModes is the order Tab and F4 walk the modes in.
var visibilityModes = []visibility.Mode{visibility.ModeWork, visibility.ModeHide, visibility.ModeOnly, visibility.ModeAll}

// visibilityCustomName names a draft that no longer fits its built-in
// name, such as Work switched to Only selected.
const visibilityCustomName = "custom"

// visibilityModeTitle is how the editor names a mode.
func visibilityModeTitle(m visibility.Mode) string {
	switch m {
	case visibility.ModeHide:
		return "Hide selected"
	case visibility.ModeOnly:
		return "Only selected"
	case visibility.ModeAll:
		return "All"
	}
	return "Work"
}

// visibilityHeading says what a mark means in mode m.
func visibilityHeading(m visibility.Mode) string {
	switch m {
	case visibility.ModeHide:
		return "[x] hides the type; every other type is listed"
	case visibility.ModeOnly:
		return "Only [x] types are listed, also when empty"
	case visibility.ModeAll:
		return "Every discovered type is listed"
	}
	return "+ lists a type, - hides it, blank keeps Work's choice"
}

// selection is a set of type selectors.
type selection map[schema.GroupResource]bool

func newSelection(list []schema.GroupResource) selection {
	s := make(selection)
	for _, gr := range list {
		s[gr] = true
	}
	return s
}

// sorted lists the selectors by group, then resource.
func (s selection) sorted() []schema.GroupResource {
	out := make([]schema.GroupResource, 0, len(s))
	for gr := range s {
		out = append(out, gr)
	}
	slices.SortFunc(out, func(a, b schema.GroupResource) int {
		return cmp.Or(cmp.Compare(a.Group, b.Group), cmp.Compare(a.Resource, b.Resource))
	})
	return out
}

// visibilityDraft is the profile being edited. Each mode keeps its own
// selection, so trying another mode never carries one mode's choices into
// another: a manual profile does not inherit Work's.
type visibilityDraft struct {
	name     string
	mode     visibility.Mode
	workShow selection
	workHide selection
	hide     selection
	only     selection
}

func newVisibilityDraft(name string, p visibility.Profile) visibilityDraft {
	d := visibilityDraft{name: name, mode: p.Mode, workShow: make(selection), workHide: make(selection), hide: make(selection), only: make(selection)}
	switch d.mode {
	case visibility.ModeHide:
		d.hide = newSelection(p.Hide)
	case visibility.ModeOnly:
		d.only = newSelection(p.Show)
	case visibility.ModeAll:
	default:
		d.mode = visibility.ModeWork
		d.workShow, d.workHide = newSelection(p.Show), newSelection(p.Hide)
	}
	return d
}

// profile is the draft of the current mode as a policy.
func (d visibilityDraft) profile() visibility.Profile {
	switch d.mode {
	case visibility.ModeHide:
		return visibility.Profile{Mode: visibility.ModeHide, Hide: d.hide.sorted()}
	case visibility.ModeOnly:
		return visibility.Profile{Mode: visibility.ModeOnly, Show: d.only.sorted()}
	case visibility.ModeAll:
		return visibility.Profile{Mode: visibility.ModeAll}
	}
	return visibility.Profile{Mode: visibility.ModeWork, Show: d.workShow.sorted(), Hide: d.workHide.sorted()}
}

// selectors lists the selectors of the current mode.
func (d visibilityDraft) selectors() []schema.GroupResource {
	switch d.mode {
	case visibility.ModeHide:
		return d.hide.sorted()
	case visibility.ModeOnly:
		return d.only.sorted()
	case visibility.ModeWork:
		all := make(selection)
		for gr := range d.workShow {
			all[gr] = true
		}
		for gr := range d.workHide {
			all[gr] = true
		}
		return all.sorted()
	}
	return nil
}

// manual is the selection the marks of Hide or Only change, nil in Work
// and All.
func (d visibilityDraft) manual() selection {
	switch d.mode {
	case visibility.ModeHide:
		return d.hide
	case visibility.ModeOnly:
		return d.only
	}
	return nil
}

// mark is the mark column of gr: "+", "-" or blank in Work, "[x]" or
// "[ ]" in Hide and Only, blank in All.
func (d visibilityDraft) mark(gr schema.GroupResource) string {
	switch d.mode {
	case visibility.ModeWork:
		switch {
		case d.workShow[gr]:
			return " + "
		case d.workHide[gr]:
			return " - "
		}
	case visibility.ModeHide, visibility.ModeOnly:
		if d.manual()[gr] {
			return "[x]"
		}
		return "[ ]"
	}
	return "   "
}

// toggle moves gr to its next state: in Work blank, +, -, blank again; in
// Hide and Only checked and unchecked. All has nothing to toggle.
func (d *visibilityDraft) toggle(gr schema.GroupResource) bool {
	switch d.mode {
	case visibility.ModeWork:
		switch {
		case d.workShow[gr]:
			delete(d.workShow, gr)
			d.workHide[gr] = true
		case d.workHide[gr]:
			delete(d.workHide, gr)
		default:
			d.workShow[gr] = true
		}
		return true
	case visibility.ModeHide, visibility.ModeOnly:
		if set := d.manual(); set[gr] {
			delete(set, gr)
		} else {
			set[gr] = true
		}
		return true
	}
	return false
}

// visibilityBulk is what F5, F6 and F8 do to the types the filter shows.
type visibilityBulk int

const (
	bulkCheck visibilityBulk = iota // Work: show; Hide and Only: check
	bulkHide                        // Work: hide
	bulkReset                       // Work: back to Work's choice; Hide and Only: uncheck
)

func (d *visibilityDraft) apply(action visibilityBulk, grs []schema.GroupResource) {
	for _, gr := range grs {
		switch d.mode {
		case visibility.ModeWork:
			delete(d.workShow, gr)
			delete(d.workHide, gr)
			switch action {
			case bulkCheck:
				d.workShow[gr] = true
			case bulkHide:
				d.workHide[gr] = true
			}
		case visibility.ModeHide, visibility.ModeOnly:
			if action == bulkCheck {
				d.manual()[gr] = true
			} else {
				delete(d.manual(), gr)
			}
		}
	}
}

// visibilityRow is one type in the editor: discovered, or a selector of the
// draft whose type discovery does not report.
type visibilityRow struct {
	gr         schema.GroupResource
	version    string
	kind       string
	shortNames []string
	namespaced bool
	// absent says why a selector has no discovered type; "" for a
	// discovered one.
	absent string
}

// searchText is what the filter matches: the plural name with the group,
// the kind and the short names.
func (r visibilityRow) searchText() string {
	return strings.ToLower(strings.Join(append([]string{visibility.Format(r.gr), r.kind}, r.shortNames...), "\x00"))
}

// VisibilityEditorModel edits which resource types a panel lists: the
// mode, Work's additions and removals, or the checked types of a Hide or
// Only profile. It reads discovery only and changes nothing until Apply or
// Save.
type VisibilityEditorModel struct {
	context string
	opened  appconfig.EffectiveVisibility
	saved   appconfig.ResourcesVisibilityConfig
	draft   visibilityDraft
	types   models.ContextTypes
	rows    []visibilityRow
	set     *visibility.Set
	visible []int
	filter  string
	// hideEmpty is the panel's "Include empty: No": a listed type that
	// is not selected by name has a row only while it has objects.
	hideEmpty bool

	selected, scroll int
	width, height    int
	// modeHit is the column range of the mode on the profile line, where
	// a click switches the mode.
	modeHit [2]int

	onApply  func(name string, p visibility.Profile) tea.Cmd
	onSave   func(name string, p visibility.Profile) tea.Cmd
	onPick   func() tea.Cmd
	onCancel func() tea.Cmd
}

// The editor has three lines above the list (the profile line, the
// heading and the column titles) and two below it (the summary and the
// filter).
const (
	visibilityEditorHeader = 3
	visibilityEditorFooter = 2
)

// NewVisibilityEditorModel opens the editor on the profile a panel lists
// under now (opened), with the panel's copy of the settings (saved) to load
// other profiles from and the context's discovered types.
func NewVisibilityEditorModel(context string, opened appconfig.EffectiveVisibility, saved appconfig.ResourcesVisibilityConfig, types models.ContextTypes) *VisibilityEditorModel {
	m := &VisibilityEditorModel{context: context, opened: opened, saved: saved.Clone(), types: types}
	m.load(opened.Name, opened.Profile)
	return m
}

// load replaces the draft with profile p named name.
func (m *VisibilityEditorModel) load(name string, p visibility.Profile) {
	m.draft = newVisibilityDraft(name, p)
	m.selected, m.scroll = 0, 0
	m.rebuildRows()
}

// LoadProfile replaces the draft with the saved profile name.
func (m *VisibilityEditorModel) LoadProfile(name string) error {
	p, err := m.saved.Profile(name)
	if err != nil {
		return err
	}
	m.opened.Err = nil
	m.load(name, p)
	return nil
}

// Saved is the panel's copy of the visibility settings the editor loads
// profiles from.
func (m *VisibilityEditorModel) Saved() appconfig.ResourcesVisibilityConfig { return m.saved }

// Context is the kube context the editor lists the types of.
func (m *VisibilityEditorModel) Context() string { return m.context }

// rebuildRows lists the discovered types and the selectors of the current
// mode that discovery does not report, in name order.
func (m *VisibilityEditorModel) rebuildRows() {
	m.rows = m.rows[:0]
	seen := make(map[schema.GroupResource]bool)
	for _, e := range m.types.Entries {
		gr := e.GVR.GroupResource()
		if seen[gr] {
			continue
		}
		seen[gr] = true
		m.rows = append(m.rows, visibilityRow{gr: gr, version: e.GVR.Version, kind: e.Kind, shortNames: e.ShortNames, namespaced: e.Namespaced})
	}
	for _, gr := range m.draft.selectors() {
		if seen[gr] {
			continue
		}
		seen[gr] = true
		absent := "not discovered"
		if slices.Contains(m.types.FailedGroups, gr.Group) {
			absent = "discovery failed"
		}
		m.rows = append(m.rows, visibilityRow{gr: gr, absent: absent})
	}
	slices.SortStableFunc(m.rows, func(a, b visibilityRow) int {
		return cmp.Or(cmp.Compare(a.gr.Resource, b.gr.Resource), cmp.Compare(a.gr.Group, b.gr.Group))
	})
	m.reevaluate()
	m.refilter()
}

// reevaluate decides every discovered type under the draft.
func (m *VisibilityEditorModel) reevaluate() {
	grs := make([]schema.GroupResource, 0, len(m.rows))
	for _, r := range m.rows {
		if r.absent == "" {
			grs = append(grs, r.gr)
		}
	}
	set, err := visibility.Evaluate(m.draft.profile(), grs)
	if err != nil {
		// The draft is built from sets, so it cannot show and hide one
		// type; Work is the narrow fallback all the same.
		set, _ = visibility.Evaluate(visibility.Work(), grs)
	}
	m.set = set
}

func (m *VisibilityEditorModel) refilter() {
	current, hadCurrent := m.current()
	q := strings.ToLower(m.filter)
	m.visible = m.visible[:0]
	for i, r := range m.rows {
		if q == "" || strings.Contains(r.searchText(), q) {
			m.visible = append(m.visible, i)
		}
	}
	m.selected = min(m.selected, max(0, len(m.visible)-1))
	if hadCurrent {
		for i, idx := range m.visible {
			if m.rows[idx].gr == current.gr {
				m.selected = i
				break
			}
		}
	}
	m.ensureVisible()
}

func (m *VisibilityEditorModel) current() (visibilityRow, bool) {
	if m.selected < 0 || m.selected >= len(m.visible) {
		return visibilityRow{}, false
	}
	return m.rows[m.visible[m.selected]], true
}

// shownTypes lists the discovered types the filter shows: what F5, F6 and
// F8 change. They never add a selector for a type that is not discovered.
func (m *VisibilityEditorModel) shownTypes() []schema.GroupResource {
	var out []schema.GroupResource
	for _, idx := range m.visible {
		if r := m.rows[idx]; r.absent == "" {
			out = append(out, r.gr)
		}
	}
	return out
}

func (m *VisibilityEditorModel) listRows() int {
	return max(1, m.height-visibilityEditorHeader-visibilityEditorFooter)
}

func (m *VisibilityEditorModel) ensureVisible() {
	rows := m.listRows()
	if m.selected < m.scroll {
		m.scroll = m.selected
	}
	if m.selected >= m.scroll+rows {
		m.scroll = m.selected - rows + 1
	}
	m.scroll = max(0, min(m.scroll, len(m.visible)-rows))
}

func (m *VisibilityEditorModel) move(delta int) {
	if len(m.visible) == 0 {
		return
	}
	m.selected = max(0, min(len(m.visible)-1, m.selected+delta))
	m.ensureVisible()
}

func (m *VisibilityEditorModel) nextMode(delta int) {
	i := slices.Index(visibilityModes, m.draft.mode)
	m.draft.mode = visibilityModes[(i+delta+len(visibilityModes))%len(visibilityModes)]
	m.rebuildRows()
}

func (m *VisibilityEditorModel) toggleCurrent() {
	if r, ok := m.current(); ok && m.draft.toggle(r.gr) {
		m.reevaluate()
	}
}

func (m *VisibilityEditorModel) bulk(action visibilityBulk) {
	if !m.bulkEnabled(action) {
		return
	}
	m.draft.apply(action, m.shownTypes())
	m.reevaluate()
}

func (m *VisibilityEditorModel) bulkEnabled(action visibilityBulk) bool {
	switch {
	case m.draft.mode == visibility.ModeAll, len(m.shownTypes()) == 0:
		return false
	case action == bulkHide:
		return m.draft.mode == visibility.ModeWork
	}
	return true
}

// Profile returns the draft's name and policy. A draft that no longer
// fits its built-in name is renamed: Work switched to Hide or Only is
// "custom", All switched to Work is "work".
func (m *VisibilityEditorModel) Profile() (string, visibility.Profile) {
	p := m.draft.profile()
	name := m.draft.name
	switch {
	case p.Mode == visibility.ModeAll && (name == appconfig.VisibilityProfileWork || name == visibilityCustomName || name == ""):
		name = appconfig.VisibilityProfileAll
	case p.Mode == visibility.ModeWork && (name == appconfig.VisibilityProfileAll || name == ""):
		name = appconfig.VisibilityProfileWork
	case p.Mode != visibility.ModeWork && name == appconfig.VisibilityProfileWork,
		p.Mode != visibility.ModeAll && name == appconfig.VisibilityProfileAll,
		name == "":
		name = visibilityCustomName
	}
	return name, p
}

// Edited reports whether the draft differs from the saved profile of its
// name, or has no saved profile.
func (m *VisibilityEditorModel) Edited() bool {
	name, p := m.Profile()
	saved, err := m.saved.Profile(name)
	return err != nil || !saved.Equal(p)
}

func (m *VisibilityEditorModel) Init() tea.Cmd { return nil }

// SetDimensions sets the content size.
func (m *VisibilityEditorModel) SetDimensions(w, h int) {
	m.width, m.height = w, h
	m.ensureVisible()
}

// PreferredSize implements ModalSizer: wide enough for long CRD names, as
// tall as the screen allows.
func (m *VisibilityEditorModel) PreferredSize(maxW, maxH int) (width, height int) {
	return min(maxW, 104), maxH
}

// HandleModalEscape clears the filter on Esc; with no filter the dialog
// closes as any other does.
func (m *VisibilityEditorModel) HandleModalEscape(tea.KeyMsg) (bool, tea.Cmd) {
	if m.filter == "" {
		return false, nil
	}
	m.filter = ""
	m.refilter()
	return true, nil
}

//nolint:gocyclo // one case per key
func (m *VisibilityEditorModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch mm := msg.(type) {
	case tea.MouseWheelMsg:
		m.move(wheelStep(mm))
		return m, nil
	case tea.MouseReleaseMsg:
		m.click(mm.Mouse())
		return m, nil
	}
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "enter":
		if m.onApply != nil {
			name, p := m.Profile()
			return m, m.onApply(name, p)
		}
	case "f2", "ctrl+s":
		if m.onSave != nil {
			name, p := m.Profile()
			return m, m.onSave(name, p)
		}
	case "f3":
		return m, call(m.onPick)
	case "f10":
		return m, call(m.onCancel)
	case "tab", "f4":
		m.nextMode(1)
	case "shift+tab":
		m.nextMode(-1)
	case "f5":
		m.bulk(bulkCheck)
	case "f6":
		m.bulk(bulkHide)
	case "f8":
		m.bulk(bulkReset)
	case "space":
		m.toggleCurrent()
	case "insert", "ctrl+t":
		// mc marks with Insert and C-t and moves on to the next row.
		m.toggleCurrent()
		m.move(1)
	case "up":
		m.move(-1)
	case "down":
		m.move(1)
	case "pgup":
		m.move(-m.listRows())
	case "pgdown":
		m.move(m.listRows())
	case "home":
		m.move(-len(m.visible))
	case "end":
		m.move(len(m.visible))
	case "backspace":
		if r := []rune(m.filter); len(r) > 0 {
			m.filter = string(r[:len(r)-1])
			m.refilter()
		}
	default:
		if key.Text != "" && key.Text != " " && key.Mod&^tea.ModShift == 0 {
			m.filter += key.Text
			m.selected = 0
			m.refilter()
		}
	}
	return m, nil
}

func call(fn func() tea.Cmd) tea.Cmd {
	if fn == nil {
		return nil
	}
	return fn()
}

// click handles a left click (content coordinates): on the mode it
// switches the mode; on a row it moves the cursor there; on the mark
// column, or on the row the cursor is on, it toggles the type.
func (m *VisibilityEditorModel) click(mouse tea.Mouse) {
	if mouse.Button != tea.MouseLeft {
		return
	}
	if mouse.Y == 0 && mouse.X >= m.modeHit[0] && mouse.X < m.modeHit[1] {
		m.nextMode(1)
		return
	}
	i := m.scroll + mouse.Y - visibilityEditorHeader
	if mouse.Y < visibilityEditorHeader || mouse.Y >= visibilityEditorHeader+m.listRows() || i >= len(m.visible) {
		return
	}
	toggle := i == m.selected || mouse.X <= 3
	m.selected = i
	if toggle {
		m.toggleCurrent()
	}
}

// FooterHints implements ModalFooterHints.
func (m *VisibilityEditorModel) FooterHints() []FooterHint {
	check, reset := "Check", "Clear"
	if m.draft.mode == visibility.ModeWork {
		check, reset = "Show", "Reset"
	}
	return []FooterHint{
		{Key: "F2", Label: "Save", Enabled: m.onSave != nil},
		{Key: "F3", Label: "Profile", Enabled: m.onPick != nil},
		{Key: "F4", Label: "Mode", Enabled: true},
		{Key: "F5", Label: check, Enabled: m.bulkEnabled(bulkCheck)},
		{Key: "F6", Label: "Hide", Enabled: m.bulkEnabled(bulkHide)},
		{Key: "F8", Label: reset, Enabled: m.bulkEnabled(bulkReset)},
		{Key: "F10", Label: "Cancel", Enabled: true},
	}
}

// visibilityColumns are the widths of the editor's columns; 0 drops one.
type visibilityColumns struct {
	name, version, scope, listed, reason int
}

const (
	visibilityReasonWidth = len("removed from Work")
	// visibilityListedWidth has a cell more than "if any", so the value
	// does not read as one phrase with the reason after it.
	visibilityListedWidth = len("if any") + 1
	// visibilityNameWant is the name width the optional columns give way
	// to: a long CRD name keeps this much before Version is dropped.
	visibilityNameWant = 30
)

// columns fits the columns to the width: the mark, the name and the
// reason always; Listed, Scope and Version, in that order, while the name
// keeps visibilityNameWant cells.
func (m *VisibilityEditorModel) columns() visibilityColumns {
	natural, version := len("Type"), len("Version")
	for _, r := range m.rows {
		natural = max(natural, lipgloss.Width(visibility.Format(r.gr)))
		version = max(version, len(r.version))
	}
	c := visibilityColumns{reason: visibilityReasonWidth}
	// " " + mark + " " + name + " " + reason
	room := m.width - 1 - 3 - 1 - 1 - c.reason
	if room < 8 {
		c.reason = max(0, c.reason-(8-room))
		room = 8
	}
	want := min(natural, visibilityNameWant)
	for _, col := range []struct {
		width int
		dst   *int
	}{{visibilityListedWidth, &c.listed}, {len("cluster"), &c.scope}, {version, &c.version}} {
		if room-(col.width+1) < want {
			break
		}
		*col.dst = col.width
		room -= col.width + 1
	}
	c.name = max(1, min(natural, room))
	return c
}

func (m *VisibilityEditorModel) formatRow(c visibilityColumns, mark, name, version, scope, listed, why string) string {
	parts := []string{mark, fit(name, c.name)}
	for _, col := range []struct {
		text  string
		width int
	}{{version, c.version}, {scope, c.scope}, {listed, c.listed}} {
		if col.width > 0 {
			parts = append(parts, fit(col.text, col.width))
		}
	}
	if c.reason > 0 {
		parts = append(parts, fit(why, c.reason))
	}
	return " " + strings.Join(parts, " ")
}

// fit cuts s to w cells with an ellipsis, or pads it to w.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = ansi.Truncate(s, w, "…")
	return s + strings.Repeat(" ", max(0, w-lipgloss.Width(s)))
}

// View renders the editor body.
func (m *VisibilityEditorModel) View() tea.View {
	row := lipgloss.NewStyle().Background(lipgloss.Color(styles.ColorModalBg)).Foreground(lipgloss.Color(styles.ColorModalFg))
	bold := row.Bold(true)
	dim := row.Foreground(lipgloss.Color(styles.ColorDarkGrey))
	sel := row.Background(lipgloss.Color(styles.ColorModalSelBg)).Bold(true)
	errStyle := row.Foreground(lipgloss.Red)
	w := max(1, m.width)
	line := func(st lipgloss.Style, s string) string { return st.Render(fit(s, w)) }

	name, _ := m.Profile()
	title := appconfig.EffectiveVisibility{Name: name, Edited: m.Edited()}.Title()
	head := " Profile: " + title + "   "
	mode := "Mode: " + visibilityModeTitle(m.draft.mode)
	m.modeHit = [2]int{lipgloss.Width(head), lipgloss.Width(head) + lipgloss.Width(mode)}
	header := head + mode
	if m.context != "" {
		header += "   Context: " + m.context
	}
	c := m.columns()
	lines := []string{
		line(row, header),
		line(bold, " "+visibilityHeading(m.draft.mode)),
		line(bold, m.formatRow(c, "   ", "Type", "Version", "Scope", "Listed", "Why")),
	}
	end := min(len(m.visible), m.scroll+m.listRows())
	for i := m.scroll; i < end; i++ {
		r := m.rows[m.visible[i]]
		listed, why, included := m.effect(r)
		scope := ""
		switch {
		case r.absent != "":
		case r.namespaced:
			scope = "ns"
		default:
			scope = "cluster"
		}
		st := row
		switch {
		case i == m.selected:
			st = sel
		case !included:
			st = dim
		}
		lines = append(lines, line(st, m.formatRow(c, m.draft.mark(r.gr), visibility.Format(r.gr), r.version, scope, listed, why)))
	}
	if len(m.visible) == 0 {
		lines = append(lines, line(dim, " No type matches the filter"))
	}
	for len(lines) < visibilityEditorHeader+m.listRows() {
		lines = append(lines, line(row, ""))
	}
	if m.opened.Err != nil {
		lines = append(lines, line(errStyle, " "+m.opened.Err.Error()+"; listing Work"))
	} else {
		lines = append(lines, line(row, " "+m.summary()))
	}
	lines = append(lines, m.filterLine(row, dim, w))
	return tea.NewView(strings.Join(lines, "\n"))
}

// filterLine shows the filter at the left and the keys that are not on
// the key bar at the right, when there is room.
func (m *VisibilityEditorModel) filterLine(row, dim lipgloss.Style, w int) string {
	left := " Filter: " + m.filter + "_"
	if m.filter != "" {
		left += fmt.Sprintf("  %d of %d", len(m.visible), len(m.rows))
	}
	right := "Space marks, Enter applies "
	if m.draft.mode == visibility.ModeAll {
		right = "Enter applies "
	}
	gap := w - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 2 {
		return row.Render(fit(left, w))
	}
	return row.Render(left+strings.Repeat(" ", gap)) + dim.Render(right)
}

// effect says whether the draft lists r and why.
func (m *VisibilityEditorModel) effect(r visibilityRow) (listed, why string, included bool) {
	if r.absent != "" {
		return "-", r.absent, false
	}
	d, ok := m.set.Decide(r.gr)
	if !ok {
		return "-", "", false
	}
	switch {
	case d.Included && m.hideEmpty && !d.Explicit:
		return "if any", d.Reason.String(), true
	case d.Included:
		return "yes", d.Reason.String(), true
	}
	return "no", d.Reason.String(), false
}

// summary counts what the draft lists.
func (m *VisibilityEditorModel) summary() string {
	listed, discovered := 0, 0
	for _, r := range m.rows {
		if r.absent != "" {
			continue
		}
		discovered++
		if d, ok := m.set.Decide(r.gr); ok && d.Included {
			listed++
		}
	}
	s := fmt.Sprintf("%d of %d types listed", listed, discovered)
	if m.filter != "" {
		// What F5, F6 and F8 would change.
		matched := 0
		for _, i := range m.visible {
			if m.rows[i].absent == "" {
				matched++
			}
		}
		s += fmt.Sprintf(", %d match the filter", matched)
	}
	if n := len(m.set.Missing()); n > 0 {
		s += fmt.Sprintf(", %d selected not discovered", n)
	}
	if len(m.types.FailedGroups) > 0 {
		s += ", discovery failed for " + strings.Join(m.types.FailedGroups, ", ")
	}
	return s
}
