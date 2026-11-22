package ui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/sttts/kc/internal/access"
	"github.com/sttts/kc/internal/filexfer"
	"github.com/sttts/kc/internal/models"
	navui "github.com/sttts/kc/internal/navigation"
)

// uploadActionName names a copy into a container in read-only refusals.
const uploadActionName = "Copy into a container"

// uploadMaxCommands bounds the kubectl cp lines the dialog shows.
const uploadMaxCommands = 2

// startUpload opens the red copy dialog when F5 is pressed in a local
// directory and the other panel shows a container folder. It reports false
// when the panels do not form an upload.
func (a *App) startUpload(panel *Panel, srcIdx int, srcNav, dstNav *navui.Navigator) (tea.Cmd, bool) {
	local, ok := srcNav.Current().(localFolder)
	if !ok {
		return nil, false
	}
	target, ok := dstNav.Current().(*models.PodContainerFSFolder)
	if !ok {
		return nil, false
	}
	if a.transfer != nil {
		return a.ShowToast("A copy is still open", 3*time.Second), true
	}
	dest := target.TransferSource()
	if dest.Pod == "" {
		return a.ShowToast("The container filesystem is unavailable", 3*time.Second), true
	}
	if cmd := a.refuseUpload(dest); cmd != nil {
		return cmd, true
	}
	ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
	defer cancel()
	names := markedLocalNames(ctx, panel)
	if len(names) == 0 {
		return a.ShowToast("Nothing to copy: mark files or directories", 3*time.Second), true
	}
	req := &filexfer.UploadRequest{Src: local.LocalDir(), Names: names, Dir: dest.Dir}
	conflicts := 0
	commands := make([]string, 0, uploadMaxCommands+1)
	for i, n := range names {
		if target.HasEntry(ctx, n) {
			conflicts++
		}
		if i < uploadMaxCommands {
			commands = append(commands, target.UploadCommand(filepath.Join(req.Src, n), n))
		}
	}
	if extra := len(names) - uploadMaxCommands; extra > 0 {
		commands = append(commands, fmt.Sprintf("(and %d more, one per entry)", extra))
	}
	model := NewUploadModel(uploadDetails(dest, req), conflicts, commands, dest)
	a.openTransfer(&transferJob{
		model:     model,
		upload:    req,
		source:    target,
		destPanel: 1 - srcIdx,
	})
	return nil, true
}

// refuseUpload refuses a copy into the container of dest in read-only
// mode: it execs into the pod and writes there. The check covers the
// current context and the one the container folder belongs to.
func (a *App) refuseUpload(dest models.ContainerFSSource) tea.Cmd {
	if cmd := a.refuseReadOnly(uploadActionName, access.ExecChecks(dest.Namespace, dest.Pod)...); cmd != nil {
		return cmd
	}
	if dest.Context != "" && a.readOnlyFor(dest.Context) {
		return a.readOnlyRefusal(uploadActionName)
	}
	return nil
}

// markedLocalNames returns the names of the marked local entries of panel,
// or the one under the cursor when nothing is marked.
func markedLocalNames(ctx context.Context, panel *Panel) []string {
	var out []string
	for _, it := range panel.GetSelectedItems() {
		if le, ok := it.Item.(models.LocalEntryItem); ok {
			out = append(out, filepath.Base(le.LocalPath()))
		}
	}
	if len(out) > 0 {
		return out
	}
	if item, ok := panel.SelectedNavItem(ctx); ok {
		if le, ok := item.(models.LocalEntryItem); ok {
			out = append(out, filepath.Base(le.LocalPath()))
		}
	}
	return out
}

func uploadDetails(dest models.ContainerFSSource, req *filexfer.UploadRequest) [][2]string {
	container := dest.Container
	if dest.Helper {
		container += " (through the debug helper)"
	}
	list := strings.Join(req.Names, ", ")
	if r := []rune(list); len(r) > transferDetailWidth {
		list = string(r[:transferDetailWidth-3]) + "..."
	}
	return [][2]string{
		{"From", shortenPath(req.Src)},
		{"Entries", list},
		{"Context", dest.Context},
		{"Pod", dest.Namespace + "/" + dest.Pod},
		{"Container", container},
		{"To", shortenPath(dest.Dir)},
	}
}
