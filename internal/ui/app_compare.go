package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/stokaro/kc/internal/manifestdiff"
	"github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/pkg/appconfig"
)

const (
	// ctrlXPrefixKey starts mc's two-key commands.
	ctrlXPrefixKey = "ctrl+x"
	// compareKey completes C-x C-d, mc's "Compare files".
	compareKey = "ctrl+d"
	// compareReadTimeout bounds reading both manifests: one GET each
	// from the API server, which a slow or remote cluster can delay.
	compareReadTimeout = 10 * time.Second
)

// handleCtrlXKey runs the command for the key typed after C-x: kc's own
// chords first, then the C-x hotkeys of custom commands. Keys without a
// command are dropped, as mc does.
func (a *App) handleCtrlXKey(press tea.KeyPressMsg) tea.Cmd {
	switch press.String() {
	case compareKey:
		return a.compareSelectedObjects()
	case compareListsKey:
		return a.compareLists()
	case compareContextKey:
		return a.compareListWithContext()
	case compareWholeListsKey:
		return a.compareWholeLists()
	}
	if cmd, ok := a.handlePinChord(press.String()); ok {
		return cmd
	}
	return a.commandChord(press)
}

// selectedManifestSource returns the object under the cursor of panel.
func selectedManifestSource(ctx context.Context, panel *Panel) (models.ManifestSource, bool) {
	if panel == nil {
		return nil, false
	}
	item, ok := panel.SelectedNavItem(ctx)
	if !ok {
		return nil, false
	}
	src, ok := item.(models.ManifestSource)
	return src, ok
}

// compareSelectedObjects opens a viewer with the diff of the objects under
// the cursors of the two panels. The manifests are read and compared in a
// command, so the UI stays responsive while the cache syncs.
func (a *App) compareSelectedObjects() tea.Cmd {
	ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
	left, okLeft := selectedManifestSource(ctx, a.leftPanel)
	right, okRight := selectedManifestSource(ctx, a.rightPanel)
	cancel()
	if !okLeft || !okRight {
		if a.toastLogger != nil {
			return a.toastLogger.Errorf("Compare: put the cursor on an object in each panel")
		}
		return nil
	}
	fallbackCtx := ""
	if a.currentCtx != nil {
		fallbackCtx = a.currentCtx.Name
	}
	var view *DiffViewer
	load := func(seq int, opts manifestdiff.Options) tea.Cmd {
		return a.readComparison(view, seq, left, right, fallbackCtx, opts)
	}
	view = NewDiffViewer(a.viewerTheme(), itemViewCommand(left), itemViewCommand(right), load, func() tea.Cmd {
		a.hideModal()
		return nil
	})
	view.SetWrapMode(a.cfg != nil && strings.EqualFold(a.cfg.Viewer.Mode, appconfig.ViewerModeWrap))
	view.SetOnOptions(func() tea.Cmd { return a.showViewerOptionsModal(view) })
	modal := NewModal(fmt.Sprintf("compare %s / %s", left.Name(), right.Name()), view)
	modal.SetMode(ModalModeFullscreen)
	modal.SetDimensions(a.width, a.height)
	modal.SetCloseOnSingleEsc(false)
	modal.SetCloseOnEscTimeout(true)
	a.modalManager.Register("compare_viewer", modal)
	a.showModal("compare_viewer")
	return view.Start()
}

// readComparison reads both manifests and renders them with opts.
func (a *App) readComparison(view *DiffViewer, seq int, left, right models.ManifestSource, fallbackCtx string, opts manifestdiff.Options) tea.Cmd {
	root := a.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(root, compareReadTimeout)
		defer cancel()
		ctrllog.FromContext(ctx).WithName("compare").V(1).Info("reading manifests",
			"left", left.GVR().String()+" "+left.Namespace()+"/"+left.Name(),
			"right", right.GVR().String()+" "+right.Namespace()+"/"+right.Name())
		l, err := readSide(ctx, left, fallbackCtx)
		if err != nil {
			return compareResultMsg{view: view, seq: seq, err: err}
		}
		r, err := readSide(ctx, right, fallbackCtx)
		if err != nil {
			return compareResultMsg{view: view, seq: seq, err: err}
		}
		snap := &compareSnapshot{left: l, right: r}
		res, err := manifestdiff.Compare(snap.left, snap.right, opts)
		return compareResultMsg{view: view, seq: seq, snap: snap, opts: opts, result: res, err: err}
	}
}

func readSide(ctx context.Context, src models.ManifestSource, fallbackCtx string) (manifestdiff.Side, error) {
	obj, err := src.Manifest(ctx)
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return manifestdiff.Side{}, fmt.Errorf("reading %s %s timed out, try again", src.GVR().Resource, src.Name())
	case errors.Is(err, models.ErrNoViewContent):
		return manifestdiff.Side{}, fmt.Errorf("%s %s has no manifest", src.GVR().Resource, src.Name())
	case err != nil:
		return manifestdiff.Side{}, fmt.Errorf("reading %s %s: %w", src.GVR().Resource, src.Name(), err)
	}
	ctxName := src.ContextName()
	if ctxName == "" {
		ctxName = fallbackCtx
	}
	return manifestdiff.Side{
		Context:   ctxName,
		GVR:       src.GVR(),
		Namespace: src.Namespace(),
		Name:      src.Name(),
		Object:    obj.Object,
	}, nil
}
