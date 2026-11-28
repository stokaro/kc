package ui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/stokaro/kc/internal/access"
	models "github.com/stokaro/kc/internal/models"
)

// F8 with marked rows deletes the marked rows, as in mc (#373); see
// docs/adr/0373-delete-marked-rows.md.

const (
	// deleteListMax is how many names of a marked set the dialog lists
	// before "and N more".
	deleteListMax = 5
	// refusalListMax is how many rows a refusal or a result line names.
	refusalListMax = 3
	// deleteSetToastTTL keeps a result line with failures long enough to
	// read the names.
	deleteSetToastTTL = 10 * time.Second
)

// deleteSet is the marked rows the delete dialog asks about: each object
// instance as its row showed it when F8 was pressed, in list order.
type deleteSet struct {
	panelIdx int
	targets  []deleteTarget
	// marks is the mark generation of the panel's list. The deleted rows
	// are unmarked only while the list still holds these marks.
	marks uint64
}

// deleteSetResultMsg reports the delete of a marked set: one result per
// target, in the order of the set.
type deleteSetResultMsg struct {
	set     deleteSet
	results []resourceDeletedMsg
}

// deleteMarkedForPanel opens the delete dialog for the marked rows of
// panel. When one of them cannot be deleted, it refuses the whole set and
// names the rows in the way: F8 deletes what the user marked, or nothing.
func (a *App) deleteMarkedForPanel(panel *Panel, marked []Item) tea.Cmd {
	panelIdx, ok := a.panelIndexFor(panel)
	if !ok || len(marked) == 0 {
		return nil
	}
	if slices.ContainsFunc(marked, func(row Item) bool { _, ok := row.Item.(models.Stoppable); return ok }) {
		return a.ShowToast(fmt.Sprintf("%d %s marked; F8 stops one port forward. Unmark them first (-)", len(marked), plural(len(marked), "row is", "rows are")), 5*time.Second)
	}
	set, notDeletable, noInstance := buildDeleteSet(panelIdx, marked)
	if len(set.targets) == 0 && len(noInstance) == 0 {
		// No marked row is an object, as F8 on such a row does nothing.
		return nil
	}
	ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
	_, conn := a.selectedRow(ctx, panel)
	cancel()
	// Each object goes on the cluster it was read from; read-only mode of
	// that context decides, whichever panel is active.
	for _, kc := range deleteContexts(set.targets, conn.name()) {
		if cmd := a.refuseReadOnly(kc, "Delete", access.Check{Verb: "delete"}); cmd != nil {
			return cmd
		}
	}
	if len(notDeletable) > 0 {
		return a.ShowToast(fmt.Sprintf("Cannot delete the marked rows: kc cannot delete %s; unmark %s first",
			listNames(notDeletable), plural(len(notDeletable), "it", "them")), 5*time.Second)
	}
	if len(noInstance) > 0 {
		return a.ShowToast(fmt.Sprintf("Cannot delete the marked rows: %s %s not carry the object's UID; refresh the panel and try again",
			listNames(noInstance), plural(len(noInstance), "does", "do")), 5*time.Second)
	}
	var denied []string
	same := sameResource(set.targets)
	for _, t := range set.targets {
		if !a.permitAccessIn(conn, access.DeleteChecks(t.accessTarget())...) {
			denied = append(denied, t.setName(same))
		}
	}
	if len(denied) > 0 {
		return a.ShowToast(fmt.Sprintf("Cannot delete the marked rows: you may not delete %s (M-k shows why); unmark %s first",
			listNames(denied), plural(len(denied), "it", "them")), 5*time.Second)
	}
	// An object whose delete takes others with it is deleted alone, after
	// its name is typed (#374).
	var named *deleteTarget
	if along := takeOthersAlong(set.targets); len(along) > 0 {
		switch {
		case len(set.targets) == 1:
			named = &set.targets[0]
		case len(along) == len(set.targets):
			return a.ShowToast(fmt.Sprintf("Cannot delete %d marked %s together: each asks for its name to be typed. Unmark all but one",
				len(set.targets), setNoun(set.targets)), 5*time.Second)
		default:
			return a.ShowToast(fmt.Sprintf("Cannot delete the marked rows together: %s %s typed. Delete %s alone",
				listNames(along), plural(len(along), "asks for its name to be", "ask for their names to be"), plural(len(along), "it", "each")), 5*time.Second)
		}
	}
	set.marks = panel.marksGeneration()
	title, names, more := deleteSetSummary(set.targets)
	a.pendingDelete, a.pendingDeleteSet = nil, &set
	return a.showDeleteDialog(func(m *DeleteConfirmModel) {
		m.ConfigureSet(title, strings.Join(deleteContexts(set.targets, ""), ", "), names, more)
	}, named)
}

// takeOthersAlong names the targets whose delete takes other objects with
// it: "namespace ux-a".
func takeOthersAlong(targets []deleteTarget) []string {
	var out []string
	for _, t := range targets {
		if takesOthersAlong(t.gvr) {
			out = append(out, t.label)
		}
	}
	return out
}

// buildDeleteSet captures the object instance of every marked row. It
// returns the rows that are no object kc can delete, and those whose row
// does not say which instance it shows, by name.
func buildDeleteSet(panelIdx int, marked []Item) (set deleteSet, notDeletable, noInstance []string) {
	set.panelIdx = panelIdx
	for _, row := range marked {
		obj, ok := row.Item.(models.ObjectItem)
		if !ok || !obj.SupportsVerb("delete") {
			notDeletable = append(notDeletable, row.Name)
			continue
		}
		target, err := deleteTargetFor(panelIdx, obj)
		if err != nil {
			noInstance = append(noInstance, objectRef(obj.Namespace(), obj.Name()))
			continue
		}
		target.rowID, _, _, _ = row.Item.Columns()
		set.targets = append(set.targets, target)
	}
	return set, notDeletable, noInstance
}

// accessTarget is what the access checks of the delete of t look at.
func (t deleteTarget) accessTarget() access.Target {
	return access.Target{Resource: t.gvr, Namespace: t.namespace, Name: t.name}
}

// setName names t in a set: namespace/name when every object of the set
// is of one resource, else with the kind, "deployment.apps shop/web".
func (t deleteTarget) setName(sameResource bool) string {
	if sameResource {
		return objectRef(t.namespace, t.name)
	}
	return t.label
}

// sameResource reports whether targets are all of one resource.
func sameResource(targets []deleteTarget) bool {
	for _, t := range targets[min(1, len(targets)):] {
		if t.gvr.GroupResource() != targets[0].gvr.GroupResource() {
			return false
		}
	}
	return true
}

// setNoun names what targets are, for "Delete 3 marked configmaps?": the
// kind of one object, the resource of several (kubectl's plural, with the
// group: "deployments.apps"), or "objects" when they are of several
// resources.
func setNoun(targets []deleteTarget) string {
	switch {
	case len(targets) == 0:
		return "objects"
	case !sameResource(targets):
		return plural(len(targets), "object", "objects")
	case len(targets) == 1:
		return targets[0].noun
	}
	gvr := targets[0].gvr
	if gvr.Group != "" {
		return gvr.Resource + "." + gvr.Group
	}
	return gvr.Resource
}

// deleteContexts lists the contexts the objects of targets live in, in
// set order; fallback when there are none.
func deleteContexts(targets []deleteTarget, fallback string) []string {
	var out []string
	for _, t := range targets {
		if !slices.Contains(out, t.context) {
			out = append(out, t.context)
		}
	}
	if len(out) == 0 && fallback != "" {
		out = []string{fallback}
	}
	return out
}

// deleteSetSummary is what the dialog shows for a set: the question with
// the count and the kind, the names of the first objects and how many more
// there are. A single name left over is listed rather than counted.
func deleteSetSummary(targets []deleteTarget) (title string, names []string, more int) {
	limit := deleteListMax
	if len(targets) == deleteListMax+1 {
		limit = len(targets)
	}
	same := sameResource(targets)
	for i, t := range targets {
		if i == limit {
			more = len(targets) - i
			break
		}
		names = append(names, t.setName(same))
	}
	return fmt.Sprintf("Delete %d marked %s?", len(targets), setNoun(targets)), names, more
}

// listNames joins the first refusalListMax names: "a, b, c and 2 more".
func listNames(names []string) string {
	if len(names) <= refusalListMax {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(names[:refusalListMax], ", "), len(names)-refusalListMax)
}

// performDeleteSet deletes the confirmed set; see executeDeleteSet.
func (a *App) performDeleteSet(set deleteSet) tea.Cmd {
	// A context may have turned read-only while the dialog was open.
	for _, t := range set.targets {
		if cmd := a.refuseReadOnly(t.context, "Delete", access.DeleteChecks(t.accessTarget())...); cmd != nil {
			return cmd
		}
	}
	root := a.ctx
	return a.withBusy("Delete", 300*time.Millisecond, func() tea.Msg {
		return executeDeleteSet(root, set)
	})
}

// executeDeleteSet deletes each object of set on the cluster it was read
// from, with its own UID precondition and request timeout, one after the
// other. A failure does not stop the others. When ctx ends (kc quits), the
// objects not tried yet report its error.
func executeDeleteSet(ctx context.Context, set deleteSet) deleteSetResultMsg {
	results := make([]resourceDeletedMsg, 0, len(set.targets))
	for _, t := range set.targets {
		if err := ctx.Err(); err != nil {
			results = append(results, resourceDeletedMsg{target: t, err: err})
			continue
		}
		reqCtx, cancel := context.WithTimeout(ctx, requestTimeout)
		results = append(results, executeDelete(reqCtx, t))
		cancel()
	}
	return deleteSetResultMsg{set: set, results: results}
}

// finishDeleteSet reports the delete of a marked set and unmarks the rows
// whose object is gone: deleted, already gone, or replaced by an object
// of the same name that the user never saw. Rows that failed otherwise
// (forbidden, refused by a webhook, timed out) stay marked, so the list
// shows what is left.
func (a *App) finishDeleteSet(msg deleteSetResultMsg) tea.Cmd {
	log := ctrllog.FromContext(a.ctx).WithName("delete")
	var gone []string
	failed := 0
	for _, r := range msg.results {
		t := r.target
		if r.err != nil {
			failed++
			log.Error(r.err, "delete of a marked object failed", "context", t.context, "gvr", t.gvr.String(), "namespace", t.namespace, "name", t.name, "uid", t.uid)
		}
		if r.err == nil || apierrors.IsNotFound(r.err) || apierrors.IsConflict(r.err) {
			gone = append(gone, t.rowID)
		}
	}
	log.Info("deleted marked objects", "deleted", len(msg.results)-failed, "failed", failed)
	if panel := a.panelByIndex(msg.set.panelIdx); panel != nil {
		ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
		panel.unmarkRows(ctx, msg.set.marks, gone)
		cancel()
		a.invalidateView("marked delete")
		a.invalidateFunctionBar("marked delete")
	}
	text, ok := deleteSetResultText(msg.results)
	if ok {
		return a.ShowInfo(text, 5*time.Second)
	}
	return a.ShowToast(text, deleteSetToastTTL)
}

// deleteSetResultText is the result line of a marked set: how many objects
// were deleted and, by name, which were not and why. ok is false when an
// object was not deleted.
func deleteSetResultText(results []resourceDeletedMsg) (text string, ok bool) {
	if len(results) == 1 {
		if results[0].err != nil {
			return deleteResultText(results[0]), false
		}
		return "Deleted " + results[0].target.label, true
	}
	targets := make([]deleteTarget, 0, len(results))
	for _, r := range results {
		targets = append(targets, r.target)
	}
	same := sameResource(targets)
	var failed []string
	for _, r := range results {
		if r.err != nil {
			failed = append(failed, fmt.Sprintf("%s (%s)", r.target.setName(same), deleteFailure(r.err)))
		}
	}
	noun := setNoun(targets)
	if len(failed) == 0 {
		return fmt.Sprintf("Deleted %d %s", len(results), noun), true
	}
	return fmt.Sprintf("Deleted %d of %d %s; not deleted: %s", len(results)-len(failed), len(results), noun, listNames(failed)), false
}

// deleteFailure says in a few words why one object of a set was not
// deleted; the log has the whole error.
func deleteFailure(err error) string {
	switch {
	case apierrors.IsNotFound(err):
		return "already gone"
	case apierrors.IsConflict(err):
		return "replaced by a new object"
	case apierrors.IsForbidden(err):
		return "forbidden"
	default:
		return err.Error()
	}
}
