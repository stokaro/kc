package ui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	kccluster "github.com/stokaro/kc/internal/cluster"
	"github.com/stokaro/kc/internal/manifestdiff"
	"github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/internal/setdiff"
	"github.com/stokaro/kc/pkg/appconfig"
	"github.com/stokaro/kc/pkg/kubeconfig"
)

const (
	// compareListsKey completes C-x d, mc's "Compare directories": the
	// object lists of the two panels.
	compareListsKey = "d"
	// compareContextKey completes C-x c: the active panel's list against
	// the same list in another context.
	compareContextKey = "c"
	// compareWholeListsKey completes C-x a: the two panels' lists in full,
	// whatever their filters and marks.
	compareWholeListsKey = "a"
	// compareListsTimeout bounds reading both lists. A list of another
	// context starts its informer first.
	compareListsTimeout = 30 * time.Second
)

// setSide is one list to compare: what it is, and the Deps to read it with.
// Deps.Cl is nil until the cluster of another context is acquired.
type setSide struct {
	set  setdiff.Set
	deps models.Deps
}

// listMetaFolder is a folder that shows the objects of one type.
type listMetaFolder interface {
	ObjectListMeta() (schema.GroupVersionResource, string, bool)
}

// panelListSide describes the object list panel idx shows; false when it
// shows something else. Unless whole is set, the side keeps what the
// panel shows: its marked rows, else its rows under an active filter.
func (a *App) panelListSide(idx int, whole bool) (setSide, bool) {
	nav := a.navigatorFor(idx)
	conn := a.ownConn(idx)
	cl := conn.cluster()
	if nav == nil || cl == nil {
		return setSide{}, false
	}
	lister, ok := nav.Current().(listMetaFolder)
	if !ok {
		return setSide{}, false
	}
	gvr, ns, _ := lister.ObjectListMeta()
	// Each side is read from its panel's own connection: the panels may
	// show two contexts.
	key := conn.clusterKey()
	deps := a.connDeps(conn, a.ensurePanelConfig(a.panelByIndex(idx)))
	set := setdiff.Set{Context: key.ContextName, GVR: gvr, Namespace: ns}
	set.AllNamespaces = ns == models.AllNamespaces && namespacedResource(cl.RESTMapper(), gvr)
	if !whole {
		ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
		set.Selection = panelSelection(ctx, a.panelByIndex(idx), gvr)
		cancel()
	}
	return setSide{set: set, deps: deps}, true
}

// uidRow is a row that knows the object instance it shows.
type uidRow interface {
	UID() types.UID
}

// selectionMember is the identity of an object row of gvr.
func selectionMember(item models.Item, gvr schema.GroupVersionResource) (setdiff.Member, bool) {
	obj, ok := item.(models.ObjectItem)
	if !ok || obj.GVR() != gvr || obj.Name() == "" {
		return setdiff.Member{}, false
	}
	m := setdiff.Member{Namespace: obj.Namespace(), Name: obj.Name()}
	if u, ok := item.(uidRow); ok {
		m.UID = u.UID()
	}
	return m, true
}

// panelSelection freezes what panel shows of gvr: the marked rows when
// there are any (the rows F5 and F8 act on), else the rows its row filter
// keeps when a filter is active, else the whole list.
func panelSelection(ctx context.Context, panel *Panel, gvr schema.GroupVersionResource) setdiff.Selection {
	if panel == nil {
		return setdiff.Selection{}
	}
	filter := ""
	if f, ok := panel.RowFilter(); ok && f.Active() {
		filter = f.Label()
	}
	var marked []setdiff.Member
	for _, item := range panel.GetSelectedItems() {
		if m, ok := selectionMember(item.Item, gvr); ok {
			marked = append(marked, m)
		}
	}
	if len(marked) > 0 {
		return setdiff.Selection{Scope: setdiff.Marked, Members: marked, Filter: filter}
	}
	folder := panel.Folder()
	if filter == "" || folder == nil {
		return setdiff.Selection{}
	}
	sel := setdiff.Selection{Scope: setdiff.Visible, Filter: filter, Members: make([]setdiff.Member, 0)}
	for _, row := range folder.Lines(ctx, 0, folder.Len(ctx)) {
		if item, ok := row.(models.Item); ok {
			if m, ok := selectionMember(item, gvr); ok {
				sel.Members = append(sel.Members, m)
			}
		}
	}
	return sel
}

// pairSelections makes a side without a selection follow the other side's:
// with a filter or marks on one panel only, the other panel contributes the
// counterparts of those rows instead of its whole list.
func pairSelections(left, right *setSide) {
	l, r := left.set.Selection.Scope, right.set.Selection.Scope
	switch {
	case l != setdiff.Whole && r == setdiff.Whole:
		right.set.Selection = setdiff.Selection{Scope: setdiff.Counterparts}
	case r != setdiff.Whole && l == setdiff.Whole:
		left.set.Selection = setdiff.Selection{Scope: setdiff.Counterparts}
	}
}

// namespacedResource reports whether gvr is namespaced; unknown counts as
// namespaced, as object lists assume.
func namespacedResource(mapper meta.RESTMapper, gvr schema.GroupVersionResource) bool {
	if mapper == nil {
		return true
	}
	gvk, err := mapper.KindFor(gvr)
	if err != nil {
		return true
	}
	mapping, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return true
	}
	return mapping.Scope.Name() == meta.RESTScopeNameNamespace
}

// compareLists compares what the two panels show (C-x d): marked rows,
// else rows under a filter, else the whole lists.
func (a *App) compareLists() tea.Cmd {
	return a.comparePanelLists(false)
}

// compareWholeLists compares the two panels' lists in full (C-x a).
func (a *App) compareWholeLists() tea.Cmd {
	return a.comparePanelLists(true)
}

func (a *App) comparePanelLists(whole bool) tea.Cmd {
	left, okL := a.panelListSide(0, whole)
	right, okR := a.panelListSide(1, whole)
	if !okL || !okR {
		return a.ShowToast("Compare lists: open an object list in each panel", 3*time.Second)
	}
	pairSelections(&left, &right)
	return a.runSetCompare(left, right, nil)
}

// compareListWithContext asks for a context and compares what the active
// panel shows with the same type and namespace there (C-x c).
func (a *App) compareListWithContext() tea.Cmd {
	return a.compareWithContextPicker(false)
}

// compareWholeListWithContext is C-x c for the whole list.
func (a *App) compareWholeListWithContext() tea.Cmd {
	return a.compareWithContextPicker(true)
}

func (a *App) compareWithContextPicker(whole bool) tea.Cmd {
	side, ok := a.panelListSide(a.activePanel, whole)
	if !ok {
		return a.ShowToast("Compare with context: open an object list in the active panel", 3*time.Second)
	}
	if a.kubeMgr == nil || a.clPool == nil {
		return nil
	}
	contexts := a.kubeMgr.GetContexts()
	choices := otherContextChoices(contexts, a.ownConn(a.activePanel).kctxOrNil())
	if len(choices) == 0 {
		return a.ShowToast("Compare with context: kubeconfig has no other context", 3*time.Second)
	}
	picker := NewScopePickerModel(choices, "", func(c scopeChoice) tea.Cmd {
		a.hideModalName("compare_context_picker")
		var i int
		if _, err := fmt.Sscan(c.Value, &i); err != nil || i < 0 || i >= len(contexts) {
			return nil
		}
		return a.compareWithContext(side, contexts[i])
	}, func() tea.Cmd {
		a.hideModalName("compare_context_picker")
		return nil
	})
	width := 30
	for _, c := range choices {
		width = max(width, len([]rune(c.Label))+len([]rune(c.Note))+6)
	}
	// The title stays short so it fits the frame; the list it compares is
	// the one the active panel shows.
	modal := NewModal("Compare with context", picker)
	a.modalManager.Register("compare_context_picker", modal)
	a.configureModalWindow(modal, picker, a.activePanel, "", min(width, 90), min(len(choices)+3, 20))
	a.modalManager.Show("compare_context_picker")
	a.invalidateView("compare context picker")
	return nil
}

// otherContextChoices lists the contexts of the compare with a context:
// all but the current one, whose list read twice is always equal. A
// choice's value is the context's index in contexts.
func otherContextChoices(contexts []*kubeconfig.Context, current *kubeconfig.Context) []scopeChoice {
	return slices.DeleteFunc(contextChoices(contexts, current), func(c scopeChoice) bool {
		var i int
		_, err := fmt.Sscan(c.Value, &i)
		return err != nil || contexts[i] == current
	})
}

// compareWithContext reads the right side from target's cluster. The
// cluster is acquired from the pool with kc's impersonation and released
// once the list is read; the pool keeps it for its TTL.
func (a *App) compareWithContext(left setSide, target *kubeconfig.Context) tea.Cmd {
	if target == nil || target.Kubeconfig == nil {
		return nil
	}
	key := a.clusterKeyFor(target)
	right := left
	right.set.Context = target.Name
	right.set.Selection = setdiff.Selection{Scope: setdiff.Counterparts}
	right.deps = a.makeDeps(nil, left.deps.AppConfig, key)
	pool := a.clPool
	acquire := func(ctx context.Context) (*kccluster.Cluster, func(), error) {
		ctrllog.FromContext(ctx).WithName("compare").Info("acquiring cluster", "context", target.Name)
		lease, err := pool.Acquire(ctx, key, nil)
		if err != nil {
			return nil, nil, fmt.Errorf("connecting to %s: %w", target.Name, err)
		}
		return lease.Cluster(), lease.Release, nil
	}
	return tea.Batch(a.ShowInfo("Reading "+right.set.Label()+"...", 5*time.Second), a.runSetCompare(left, right, acquire))
}

// setCompareMsg is the result of one comparison.
type setCompareMsg struct {
	seq         int
	report      setdiff.Report
	left, right models.Deps
	err         error
}

// runSetCompare reads both lists and compares them in a command. acquire,
// when set, connects the right side's cluster first and returns its
// release. Only the newest comparison is shown.
func (a *App) runSetCompare(left, right setSide, acquire func(context.Context) (*kccluster.Cluster, func(), error)) tea.Cmd {
	a.setCompareSeq++
	seq, root := a.setCompareSeq, a.ctx
	opts := manifestdiff.Options{}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(root, compareListsTimeout)
		defer cancel()
		if acquire != nil {
			cl, release, err := acquire(ctx)
			if err != nil {
				return setCompareMsg{seq: seq, err: err}
			}
			defer release()
			right.deps.Cl = cl
		}
		report, err := readAndCompare(ctx, left, right, opts)
		return setCompareMsg{seq: seq, report: report, left: left.deps, right: right.deps, err: err}
	}
}

// readAndCompare lists both sides and compares them. A side that cannot be
// read fails the comparison: its objects are unknown, not missing.
func readAndCompare(ctx context.Context, left, right setSide, opts manifestdiff.Options) (setdiff.Report, error) {
	// Refuse before reading when the sets cannot be paired.
	if _, err := setdiff.Compare(left.set, right.set, opts); err != nil {
		return setdiff.Report{}, err
	}
	for _, s := range []*setSide{&left, &right} {
		objs, err := models.ListObjects(ctx, s.deps, s.set.GVR, s.set.Namespace)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				err = errors.New("timed out")
			}
			return setdiff.Report{}, fmt.Errorf("reading %s: %w", s.set.Label(), err)
		}
		s.set.Objects = objs
	}
	return setdiff.Compare(left.set, right.set, opts)
}

// applySetCompare shows the result of the newest comparison.
func (a *App) applySetCompare(msg setCompareMsg) tea.Cmd {
	if msg.seq != a.setCompareSeq {
		return nil
	}
	if msg.err != nil {
		return a.ShowToast("Compare lists: "+msg.err.Error(), 6*time.Second)
	}
	a.showSetCompare(msg)
	return a.ShowInfo(msg.report.Summary()+" | "+msg.report.Scopes(), 8*time.Second)
}

// setCompareChoices lists the rows of a report for the picker: the state,
// then the key; an unavailable row says why.
func setCompareChoices(r setdiff.Report) []scopeChoice {
	out := make([]scopeChoice, 0, len(r.Rows))
	for i, row := range r.Rows {
		out = append(out, scopeChoice{Label: fmt.Sprintf("%-11s %s", row.State, row.Key), Value: fmt.Sprint(i), Note: row.Reason})
	}
	return out
}

// setCompareTitle names both sides, and the paired namespaces.
func setCompareTitle(r setdiff.Report) string {
	title := "left " + r.Left.Label() + " [" + r.LeftCount.String() + "] | right " + r.Right.Label() + " [" + r.RightCount.String() + "]"
	if r.PairedNamespaces {
		title += " (metadata.namespace ignored)"
	}
	return title
}

// showSetCompare opens the result list. Enter on a pair opens its diff;
// Esc closes the list.
func (a *App) showSetCompare(msg setCompareMsg) {
	r := msg.report
	choices := setCompareChoices(r)
	if len(choices) == 0 {
		choices = []scopeChoice{{Label: "nothing to compare: " + r.Scopes(), Value: "-1"}}
	}
	picker := NewScopePickerModel(choices, "", func(c scopeChoice) tea.Cmd {
		var i int
		if _, err := fmt.Sscan(c.Value, &i); err != nil || i < 0 || i >= len(r.Rows) {
			return nil
		}
		row := r.Rows[i]
		switch {
		case row.Left != nil && row.Right != nil:
			return a.openPairDiff(row, msg)
		case row.Left != nil:
			return a.ShowInfo(row.Key+" is only in "+r.Left.Label(), 3*time.Second)
		case row.Right != nil:
			return a.ShowInfo(row.Key+" is only in "+r.Right.Label(), 3*time.Second)
		default:
			return a.ShowInfo(row.Reason, 4*time.Second)
		}
	}, func() tea.Cmd {
		a.hideModalName("compare_lists")
		return nil
	})
	modal := NewModal(setCompareTitle(r), picker)
	modal.SetMode(ModalModeFullscreen)
	modal.SetDimensions(a.width, a.height)
	a.modalManager.Register("compare_lists", modal)
	a.showModal("compare_lists")
}

// openPairDiff opens the manifest diff of one matched pair, from the
// objects the comparison read. It sits on top of the result list.
func (a *App) openPairDiff(row setdiff.Row, msg setCompareMsg) tea.Cmd {
	snap := &compareSnapshot{left: *row.Left, right: *row.Right}
	var view *DiffViewer
	load := func(seq int, opts manifestdiff.Options) tea.Cmd {
		return func() tea.Msg {
			res, err := manifestdiff.Compare(snap.left, snap.right, opts)
			return compareResultMsg{view: view, seq: seq, snap: snap, opts: opts, result: res, err: err}
		}
	}
	view = NewDiffViewer(a.viewerTheme(), sideViewCommand(msg.left, *row.Left), sideViewCommand(msg.right, *row.Right), load, func() tea.Cmd {
		a.hideModalName("compare_lists_diff")
		return nil
	})
	view.SetWrapMode(a.cfg != nil && strings.EqualFold(a.cfg.Viewer.Mode, appconfig.ViewerModeWrap))
	view.SetOnOptions(func() tea.Cmd { return a.showViewerOptionsModal(view) })
	modal := NewModal("compare "+row.Key, view)
	modal.SetMode(ModalModeFullscreen)
	modal.SetDimensions(a.width, a.height)
	modal.SetCloseOnSingleEsc(false)
	modal.SetCloseOnEscTimeout(true)
	a.modalManager.Register("compare_lists_diff", modal)
	a.showModal("compare_lists_diff")
	return view.Start()
}

// sideViewCommand is the kubectl command that prints one side's manifest.
func sideViewCommand(deps models.Deps, s manifestdiff.Side) string {
	args := []string{"get", resourceArg(s.GVR), s.Name}
	if s.Namespace != "" {
		args = append(args, "-n", s.Namespace)
	}
	return models.KubectlCommand(deps, append(args, "-o", "yaml")...)
}

func resourceArg(gvr schema.GroupVersionResource) string {
	if gvr.Group == "" {
		return gvr.Resource
	}
	return gvr.Resource + "." + gvr.Version + "." + gvr.Group
}
