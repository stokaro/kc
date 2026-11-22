package models

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/duration"

	"github.com/stokaro/kc/internal/portforward"
	table "github.com/stokaro/kc/internal/table"
)

// PortForwardsPathSegment is the root entry listing the active forwards.
const PortForwardsPathSegment = "port-forwards"

// Stoppable rows end something kc runs locally when F8 is pressed. The UI
// confirms StopAction like any other change.
type Stoppable interface {
	Item
	StopAction() ConfirmedAction
}

// PortForwardsFolder lists the active port forwards of this kc process,
// across all contexts.
type PortForwardsFolder struct {
	*BaseFolder
	registry *portforward.Registry
	now      func() time.Time
}

// NewPortForwardsFolder lists the forwards of registry.
func NewPortForwardsFolder(deps Deps, registry *portforward.Registry, path []string) *PortForwardsFolder {
	cols := []table.Column{{Title: " Local"}, {Title: "Target"}, {Title: "Pod"}, {Title: "Namespace"}, {Title: "Context"}, {Title: "Age"}}
	f := &PortForwardsFolder{BaseFolder: NewBaseFolder(deps, cols, path), registry: registry, now: time.Now}
	f.SetPopulate(f.populate)
	return f
}

// RegisterDirtyListener also repaints the folder when a forward starts or
// ends. The registry subscription lives as long as the panel listens.
func (f *PortForwardsFolder) RegisterDirtyListener(fn func()) func() {
	cancel := f.BaseFolder.RegisterDirtyListener(fn)
	unsubscribe := f.registry.Subscribe(f.Refresh)
	return func() {
		unsubscribe()
		if cancel != nil {
			cancel()
		}
	}
}

func (f *PortForwardsFolder) populate(context.Context) ([]table.Row, error) {
	forwards := f.registry.List()
	rows := make([]table.Row, 0, len(forwards))
	for _, fwd := range forwards {
		rows = append(rows, newPortForwardItem(f.Deps, f.registry, fwd, f.Path(), f.now()))
	}
	return rows, nil
}

// PortForwardItem is one active forward.
type PortForwardItem struct {
	*RowItem
	registry *portforward.Registry
	fwd      portforward.Forward
}

func newPortForwardItem(deps Deps, registry *portforward.Registry, fwd portforward.Forward, parent []string, now time.Time) *PortForwardItem {
	age := duration.HumanDuration(now.Sub(fwd.Started))
	cells := []string{fwd.Local(), fwd.Spec.Target(), fmt.Sprintf("%s:%d", fwd.Spec.Pod, fwd.Spec.RemotePort), fwd.Spec.Namespace, fwd.Spec.Context, age}
	path := append(append([]string(nil), parent...), fwd.ID)
	item := &PortForwardItem{RowItem: NewRowItem(fwd.ID, cells, path, WhiteStyle()), registry: registry, fwd: fwd}
	item.details = fmt.Sprintf("%s -> %s", fwd.Local(), fwd.Spec.Target())
	item.SetViewCommand(fwd.Spec.Command)
	return item
}

// Forward returns the forward the row shows.
func (i *PortForwardItem) Forward() portforward.Forward { return i.fwd }

// ViewContent describes the forward for F3.
func (i *PortForwardItem) ViewContent() (string, string, string, string, string, error) {
	s := i.fwd.Spec
	var b strings.Builder
	line := func(k, v string) {
		if v != "" {
			fmt.Fprintf(&b, "%s: %s\n", k, v)
		}
	}
	line("local", i.fwd.Local())
	line("target", s.Target())
	line("pod", s.Pod)
	line("podPort", strconv.Itoa(int(s.RemotePort)))
	line("container", s.Container)
	line("namespace", s.Namespace)
	line("context", s.Context)
	line("started", i.fwd.Started.Format(time.RFC3339))
	line("kubectl", s.Command)
	title := "port-forward " + i.fwd.Local()
	return title, b.String(), "yaml", "application/yaml", "port-forward-" + i.fwd.ID + ".yaml", nil
}

// StopAction asks to stop the forward.
func (i *PortForwardItem) StopAction() ConfirmedAction {
	return &stopPortForward{PortForwardItem: i}
}

type stopPortForward struct{ *PortForwardItem }

func (s *stopPortForward) Confirmation() Confirmation {
	spec := s.fwd.Spec
	return Confirmation{
		Title: "Stop port forward?",
		Details: [][2]string{
			{"Local", s.fwd.Local()},
			{"Target", spec.Target()},
			{"Pod", spec.Pod},
			{"Namespace", spec.Namespace},
			{"Context", spec.Context},
		},
		Notes:  []string{"Open connections through the forward are closed."},
		Action: "Stop",
	}
}

func (s *stopPortForward) Confirm() error {
	if !s.registry.Stop(s.fwd.ID) {
		return fmt.Errorf("port forward %s already ended", s.fwd.Local())
	}
	return nil
}

// portForwardsRootRow is the "/port-forwards" entry at a panel root.
func portForwardsRootRow(deps Deps, parent []string) (table.Row, bool) {
	reg := deps.PortForwards
	if reg == nil {
		return nil, false
	}
	n := reg.Len()
	if n == 0 {
		return nil, false
	}
	path := append(append([]string(nil), parent...), PortForwardsPathSegment)
	item := &portForwardsEntry{
		RowItem: NewRowItem(PortForwardsPathSegment, []string{"/" + PortForwardsPathSegment, "", strconv.Itoa(n)}, path, WhiteStyle()),
		enter:   func() (Folder, error) { return NewPortForwardsFolder(deps, reg, path), nil },
	}
	item.details = fmt.Sprintf("%d active port forwards", n)
	if n == 1 {
		item.details = "1 active port forward"
	}
	return item, true
}

type portForwardsEntry struct {
	*RowItem
	enter func() (Folder, error)
}

func (e *portForwardsEntry) Enter() (Folder, error) { return e.enter() }
