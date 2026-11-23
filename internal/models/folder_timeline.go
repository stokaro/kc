package models

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"

	table "github.com/stokaro/kc/internal/table"
	"github.com/stokaro/kc/internal/timeline"
)

// TimelineSegment is the path segment of an object's timeline.
const TimelineSegment = "timeline"

// timelineNote says what a timeline is and is not.
const timelineNote = "Only revisions kc observed through the shared cache while recording; changes between two notifications, and before the recording, are not known. resourceVersion is an opaque token."

// timelineRow is the /timeline row inside an object at path.
func timelineRow(deps Deps, gvr schema.GroupVersionResource, namespace, name string, path []string) table.Row {
	p := append(slices.Clone(path), TimelineSegment)
	item := NewChildResourceItem(namespace+"/"+name+"/"+TimelineSegment, []string{"/" + TimelineSegment}, p, func() (Folder, error) {
		return newTimelineFolder(deps, gvr, namespace, name, p), nil
	})
	item.RowItem.details = "Record and compare the revisions of this object while you investigate"
	return item
}

// TimelineFolder shows the recording of one object. Opening it starts the
// recording, closing it does not stop it: "stop recording" does.
type TimelineFolder struct {
	*BaseFolder
	gvr             schema.GroupVersionResource
	namespace, name string
	rec             *recording
	unsub           func()
}

func newTimelineFolder(deps Deps, gvr schema.GroupVersionResource, namespace, name string, path []string) *TimelineFolder {
	base := NewBaseFolder(deps, timelineColumns(), path)
	f := &TimelineFolder{BaseFolder: base, gvr: gvr, namespace: namespace, name: name}
	f.rec = recordingFor(deps, gvr, namespace, name)
	base.SetRowSource(newSliceRowSource(f.populate))
	f.unsub = f.rec.subscribe(f.Refresh)
	return f
}

// timelineColumns are the timeline's columns. The change is free text: it
// takes the rest of the line and, in a narrow pane, gives up width first,
// down to reasonMinWidth; then Kind and ResourceVersion narrow. The
// observed time is not cut. The bottom status line shows the whole change.
func timelineColumns() []table.Column {
	return []table.Column{{Title: " Observed"}, {Title: "Kind", MinWidth: 6}, {Title: "ResourceVersion", MinWidth: 6}, {Title: "Change", Fill: true, MinWidth: reasonMinWidth}}
}

// Close stops following the recording; the recording keeps running.
func (f *TimelineFolder) Close() {
	if f.unsub != nil {
		f.unsub()
		f.unsub = nil
	}
}

// KubectlCommand watches the same object.
func (f *TimelineFolder) KubectlCommand() string {
	args := []string{"get", kubectlResource(f.gvr), f.name}
	if f.namespace != "" {
		args = append(args, "-n", f.namespace)
	}
	return kubectlCommand(f.Deps, append(args, "--watch", "--output-watch-events", "-o", "yaml")...)
}

func (f *TimelineFolder) populate(context.Context) ([]table.Row, error) {
	s := f.rec.tl.Snapshot()
	status, stopped := f.rec.state()
	rows := timelineSummaryRows(f.Path(), s, status, stopped)
	for i := range slices.Backward(s.Entries) {
		rows = append(rows, timelineEntryRows(f.Path(), s, i)...)
	}
	rows = append(rows, f.historyRows()...)
	note := NewRowItem("note", []string{"note", "", "", timelineNote}, append(slices.Clone(f.Path()), "note"), DimStyle())
	note.details = timelineNote
	rows = append(rows, note)
	if !stopped {
		rows = append(rows, &stopRecordingItem{
			RowItem: NewRowItem("stop", []string{"stop recording", "", "", "Enter stops the recording; the revisions stay until /timeline is opened again"}, append(slices.Clone(f.Path()), "stop"), WhiteStyle()),
			rec:     f.rec,
		})
	}
	return rows, nil
}

func timelineSummaryRows(path []string, s timeline.Snapshot, status string, stopped bool) []table.Row {
	stamp := func(t time.Time) string { return t.UTC().Format(time.RFC3339) }
	recording := "since " + stamp(s.Started)
	switch {
	case status != "" && stopped:
		recording += "; not running: " + status
	case stopped:
		recording += ", stopped " + stamp(s.Stopped)
	case status != "":
		recording += " (" + status + ")"
	}
	coverage := "no revision observed yet"
	if n := len(s.Entries); n > 0 {
		coverage = fmt.Sprintf("%d entries from %s to %s", n, stamp(s.Entries[0].Observed), stamp(s.Entries[n-1].Observed))
	}
	if s.Evicted > 0 {
		coverage += fmt.Sprintf("; %d older entries evicted (the newest of them observed %s)", s.Evicted, stamp(s.EvictedUntil))
	}
	if s.Duplicates+s.Churn > 0 {
		coverage += fmt.Sprintf("; %d duplicate and %d managedFields-only notifications not recorded", s.Duplicates, s.Churn)
	}
	limits := fmt.Sprintf("%d entries or %s per object; %s used", s.Limits.MaxEntries, formatBytes(int64(s.Limits.MaxBytes)), formatBytes(int64(s.Bytes)))
	out := make([]table.Row, 0, 3)
	for _, r := range []struct{ id, label, value string }{
		{"recording", "recording", recording}, {"coverage", "coverage", coverage}, {"limits", "limits", limits},
	} {
		row := NewRowItem("summary/"+r.id, []string{r.label, "", "", r.value}, append(slices.Clone(path), r.label), WhiteStyle())
		row.details = r.label + ": " + r.value
		out = append(out, row)
	}
	return out
}

// entryKind labels an entry. A change says which part changed, so a status
// transition is never shown as a spec change.
func entryKind(e timeline.Entry) string {
	if e.Kind != timeline.KindChange {
		return e.Kind.String()
	}
	var parts []string
	if e.SpecChanged {
		parts = append(parts, "spec")
	}
	if e.MetaChanged {
		parts = append(parts, "metadata")
	}
	if e.StatusChanged {
		parts = append(parts, "status")
	}
	if len(parts) == 0 {
		return "metadata"
	}
	return strings.Join(parts, "+")
}

// timelineEntryRows renders entry i: one row whose F3 shows the
// configuration diff, or the status diff for a status-only change, and one
// more row for the status diff when both changed.
func timelineEntryRows(path []string, s timeline.Snapshot, i int) []table.Row {
	e := s.Entries[i]
	id := fmt.Sprintf("rev/%d", e.Seq)
	observed := e.Observed.UTC().Format(time.RFC3339)
	style := WhiteStyle()
	if e.Kind == timeline.KindGap || e.Kind == timeline.KindDeleted {
		style = &diagWarningStyle
	}
	statusOnly := e.Kind == timeline.KindChange && e.StatusChanged && !e.SpecChanged && !e.MetaChanged
	part := timeline.PartConfig
	if statusOnly {
		part = timeline.PartStatus
	}
	change := strings.Join(e.Summary, "; ")
	row := NewSimpleItem(id, []string{observed, entryKind(e), e.ResourceVersion, change}, append(slices.Clone(path), id), style)
	row.WithViewContent(timelineDiffView(s, i, part))
	row.RowItem.details = fmt.Sprintf("revision %d, uid %s: %s", e.Seq, e.UID, change)
	rows := []table.Row{row}
	if e.Kind == timeline.KindChange && e.StatusChanged && !statusOnly {
		st := NewSimpleItem(id+"/status", []string{"", "status", e.ResourceVersion, "status of revision " + fmt.Sprint(e.Seq)}, append(slices.Clone(path), id, "status"), DimStyle())
		st.WithViewContent(timelineDiffView(s, i, timeline.PartStatus))
		st.RowItem.details = "F3 shows the status changes of this revision"
		rows = append(rows, st)
	}
	return rows
}

func timelineDiffView(s timeline.Snapshot, i int, part timeline.Part) ViewContentFunc {
	//revive:disable-next-line:function-result-limit the Viewable interface fixes these six results
	return func() (title, body, lang, mime, filename string, err error) {
		text, err := s.Diff(i, part)
		if err != nil {
			return "", "", "", "", "", err
		}
		e := s.Entries[i]
		what := "configuration"
		if part == timeline.PartStatus {
			what = "status"
		}
		title = fmt.Sprintf("%s %s revision %d (%s)", kubectlResource(s.Identity.GVR), s.Identity.Name, e.Seq, what)
		return title, text, "diff", "text/x-diff", fmt.Sprintf("%s-%d.diff", s.Identity.Name, e.Seq), nil
	}
}

// historyRows lists the ReplicaSets or ControllerRevisions the object
// controls: kept by its controller, they show earlier templates from before
// the recording.
func (f *TimelineFolder) historyRows() []table.Row {
	refs := f.rec.historyRefs()
	if len(refs) == 0 {
		return nil
	}
	out := make([]RelatedRef, 0, len(refs))
	for _, h := range refs {
		rel := "retained by the controller, created " + h.created.UTC().Format(time.RFC3339)
		if h.revision != "" {
			rel = "revision " + h.revision + ", " + rel
		}
		out = append(out, RelatedRef{GVR: f.rec.hgvr, Namespace: f.namespace, Name: h.name, Relation: rel})
	}
	return relatedRowsPrefixed(f.Deps, nil, f.Path(), "history/", out)
}

// stopRecordingItem stops the recording on Enter and stays in the folder.
type stopRecordingItem struct {
	*RowItem
	rec *recording
}

// Enter stops the recording. It opens nothing.
func (i *stopRecordingItem) Enter() (Folder, error) {
	i.rec.Stop()
	return nil, nil
}
