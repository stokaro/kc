package ui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/stokaro/kc/internal/filexfer"
	"github.com/stokaro/kc/internal/models"
	navui "github.com/stokaro/kc/internal/navigation"
	"github.com/stokaro/kc/internal/podfs"
)

const (
	transferModalKey     = "transfer"
	transferTickInterval = 200 * time.Millisecond
	// transferSessionTimeout bounds opening the filesystem session; the
	// copy itself runs until it ends or the user cancels it.
	transferSessionTimeout = 30 * time.Second
)

// transferTickMsg redraws the progress while copy gen runs.
type transferTickMsg struct{ gen int }

// transferDoneMsg reports the end of copy gen.
type transferDoneMsg struct {
	gen     int
	results []filexfer.Result
	err     error
}

// transferJob is the copy the dialog shows. There is at most one.
type transferJob struct {
	gen   int
	model *TransferModel
	req   filexfer.Request
	// upload is set for a copy from a local directory into a container;
	// req is then unused and source is the container folder.
	upload    *filexfer.UploadRequest
	source    transferSource
	destPanel int
	counter   *filexfer.Counter
	cancel    context.CancelFunc
	running   bool
}

// transferSource opens the container filesystem session a copy reads.
// *models.PodContainerFSFolder implements it.
type transferSource interface {
	TransferSession(ctx context.Context) (podfs.ExecSession, error)
	InvalidateSession()
}

// localFolder is a panel folder of the local filesystem.
type localFolder interface {
	models.Folder
	LocalDir() string
}

// startTransfer opens the copy dialog when F5 is pressed in a container
// folder and the other panel shows a local directory, or the other way
// round. It reports false when the panels do not form a copy, so F5 keeps
// its export behavior.
func (a *App) startTransfer(panel *Panel) (tea.Cmd, bool) {
	srcIdx, ok := a.panelIndexFor(panel)
	if !ok {
		return nil, false
	}
	srcNav, dstNav := a.navigatorForPanel(panel), a.navigatorForPanel(a.panelByIndex(1-srcIdx))
	if srcNav == nil || dstNav == nil {
		return nil, false
	}
	source, ok := srcNav.Current().(*models.PodContainerFSFolder)
	if !ok {
		return a.startUpload(panel, srcIdx, srcNav, dstNav)
	}
	dest, ok := dstNav.Current().(localFolder)
	if !ok {
		return nil, false
	}
	if a.transfer != nil {
		return a.ShowToast("A copy is still open", 3*time.Second), true
	}
	ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
	entries := markedFSEntries(ctx, panel)
	cancel()
	if len(entries) == 0 {
		return a.ShowToast("Nothing to copy: mark files or directories", 3*time.Second), true
	}
	src := source.TransferSource()
	req := filexfer.Request{Dir: src.Dir, Entries: entries, Dest: dest.LocalDir()}
	conflicts := 0
	for _, e := range entries {
		if _, err := os.Lstat(filepath.Join(req.Dest, e.Name)); err == nil {
			conflicts++
		}
	}
	a.openTransfer(&transferJob{
		model:     NewTransferModel(transferDetails(src, req), conflicts),
		req:       req,
		source:    source,
		destPanel: 1 - srcIdx,
	})
	return nil, true
}

// openTransfer shows the copy dialog for job.
func (a *App) openTransfer(job *transferJob) {
	a.transferGen++
	job.gen = a.transferGen
	a.transfer = job
	modal := NewModal("Copy", job.model)
	// A copy into a container changes the cluster: red, like every
	// confirmation of a change.
	modal.SetDanger(job.upload != nil)
	a.modalManager.Register(transferModalKey, modal)
	a.layoutTransferModal()
	a.modalManager.Show(transferModalKey)
	a.invalidateView("transfer dialog")
}

// markedFSEntries returns the marked container entries of panel, or the one
// under the cursor when nothing is marked.
func markedFSEntries(ctx context.Context, panel *Panel) []podfs.FileEntry {
	var out []podfs.FileEntry
	for _, it := range panel.GetSelectedItems() {
		if fe, ok := it.Item.(models.FSEntryItem); ok {
			out = append(out, fe.FSEntry())
		}
	}
	if len(out) > 0 {
		return out
	}
	if item, ok := panel.SelectedNavItem(ctx); ok {
		if fe, ok := item.(models.FSEntryItem); ok {
			out = append(out, fe.FSEntry())
		}
	}
	return out
}

func transferDetails(src models.ContainerFSSource, req filexfer.Request) [][2]string {
	container := src.Container
	if src.Helper {
		container += " (through the debug helper)"
	}
	names := make([]string, 0, len(req.Entries))
	for _, e := range req.Entries {
		n := e.Name
		if e.Type == podfs.EntryTypeDir {
			n += "/"
		}
		names = append(names, n)
	}
	list := strings.Join(names, ", ")
	if len(list) > transferDetailWidth {
		list = list[:transferDetailWidth-3] + "..."
	}
	return [][2]string{
		{"Context", src.Context},
		{"Pod", src.Namespace + "/" + src.Pod},
		{"Container", container},
		{"From", shortenPath(src.Dir)},
		{"Entries", list},
		{"To", shortenPath(req.Dest)},
	}
}

// transferDetailWidth keeps the dialog narrower than a panel.
const transferDetailWidth = 44

// shortenPath keeps the end of a long path, which names the directory.
func shortenPath(p string) string {
	r := []rune(p)
	if len(r) <= transferDetailWidth {
		return p
	}
	return "..." + string(r[len(r)-(transferDetailWidth-3):])
}

func (a *App) layoutTransferModal() {
	job := a.transfer
	if job == nil {
		return
	}
	modal := a.modalManager.modals[transferModalKey]
	if modal == nil {
		return
	}
	w, h := job.model.size()
	a.configureModalWindow(modal, job.model, a.activePanel, "", min(w+2, max(30, a.width-6)), h+2)
}

func transferTick(gen int) tea.Cmd {
	return tea.Tick(transferTickInterval, func(time.Time) tea.Msg { return transferTickMsg{gen: gen} })
}

// handleTransferMsg runs the copy dialog's state machine.
//
//nolint:gocyclo // the transfer flow message switch, one short case per message type
func (a *App) handleTransferMsg(msg tea.Msg) tea.Cmd {
	job := a.transfer
	switch msg := msg.(type) {
	case transferChoiceMsg:
		if job == nil || job.running {
			return nil
		}
		if !msg.ok {
			a.closeTransfer()
			return nil
		}
		if job.upload != nil {
			// The context may have become read-only while the dialog was open.
			if cmd := a.refuseUpload(job.model.target); cmd != nil {
				a.closeTransfer()
				return cmd
			}
			job.upload.Policy = msg.policy
		}
		job.req.Policy = msg.policy
		return a.runTransfer(job)
	case transferTickMsg:
		if job == nil || job.gen != msg.gen || !job.running {
			return nil
		}
		a.invalidateView("transfer progress")
		return transferTick(msg.gen)
	case transferCancelMsg:
		if job != nil && job.running && job.cancel != nil {
			job.cancel()
			a.layoutTransferModal()
			a.invalidateView("transfer cancel")
		}
		return nil
	case transferDoneMsg:
		if job == nil || job.gen != msg.gen {
			return nil
		}
		job.running = false
		if job.cancel != nil {
			job.cancel()
		}
		job.model.finish(msg.results, msg.err)
		a.refreshPanelAfterEdit(job.destPanel)
		log := ctrllog.FromContext(a.ctx).WithName("transfer")
		if job.upload != nil {
			log.Info("copy into container finished", "src", job.upload.Src, "dir", job.upload.Dir, "summary", summarizeResults(msg.results), "err", msg.err)
		} else {
			a.lastCopyDir = job.req.Dest
			log.Info("copy from container finished", "dest", job.req.Dest, "summary", summarizeResults(msg.results), "err", msg.err)
		}
		a.layoutTransferModal()
		a.invalidateView("transfer done")
		return nil
	case transferCloseMsg:
		if job != nil && !job.running {
			a.closeTransfer()
		}
		return nil
	}
	return nil
}

// runTransfer starts the copy in the background.
func (a *App) runTransfer(job *transferJob) tea.Cmd {
	ctx, cancel := context.WithCancel(a.ctx)
	job.cancel = cancel
	job.counter = &filexfer.Counter{}
	job.running = true
	job.model.start(job.counter.Snapshot)
	a.layoutTransferModal()
	a.invalidateView("transfer start")
	gen, req, source, counter := job.gen, job.req, job.source, job.counter
	var upload *filexfer.UploadRequest
	if job.upload != nil {
		u := *job.upload
		upload = &u
	}
	run := func() tea.Msg {
		sessCtx, cancelSess := context.WithTimeout(ctx, transferSessionTimeout)
		sess, err := source.TransferSession(sessCtx)
		cancelSess()
		if err != nil {
			return transferDoneMsg{gen: gen, err: err}
		}
		var results []filexfer.Result
		if upload != nil {
			dst, ok := sess.(filexfer.Destination)
			if !ok {
				return transferDoneMsg{gen: gen, err: errors.New("this filesystem session cannot copy into the container")}
			}
			results, err = filexfer.Upload(ctx, dst, *upload, counter)
		} else {
			results, err = filexfer.Download(ctx, sess, req, counter)
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			// A canceled read leaves the session's command stream in an
			// unknown state; the next listing opens a new one.
			source.InvalidateSession()
		}
		return transferDoneMsg{gen: gen, results: results, err: err}
	}
	return tea.Batch(run, transferTick(gen))
}

func (a *App) closeTransfer() {
	if a.transfer != nil && a.transfer.cancel != nil {
		a.transfer.cancel()
	}
	a.transfer = nil
	a.hideModalName(transferModalKey)
	a.invalidateView("transfer closed")
}

// toggleLocalPanel opens the local directory of the last copy (or the
// working directory) in the active panel, or leaves it again (M-l).
// The panel gets one folder per directory level, so Back walks up the
// tree and, from the filesystem root, returns to the cluster.
func (a *App) toggleLocalPanel() tea.Cmd {
	panel := a.activePanelRef()
	if panel == nil {
		return nil
	}
	if a.activePanel == 0 && a.leftNav == nil {
		a.leftNav = a.newScopedNavigator(0)
	}
	if a.activePanel == 1 && a.rightNav == nil {
		a.rightNav = a.newScopedNavigator(1)
	}
	nav := a.navigatorForPanel(panel)
	if nav == nil {
		return nil
	}
	if isLocalFolder(nav) {
		for isLocalFolder(nav) && nav.HasBack() {
			a.handleFolderNav(true, "", nil)
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
	selID := panel.currentSelectionID(ctx)
	cancel()
	chain := models.LocalAncestors(models.Deps{Ctx: a.ctx}, a.defaultCopyDir())
	for i, f := range chain {
		id := selID
		if i > 0 {
			id = f.LocalDir()
		}
		a.handleFolderNav(false, id, f)
	}
	return nil
}

func isLocalFolder(nav *navui.Navigator) bool {
	_, ok := nav.Current().(localFolder)
	return ok
}
