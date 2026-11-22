package models

import (
	"context"
	"errors"
	"fmt"
	"path"

	"github.com/stokaro/kc/internal/podfs"
)

// FSEntryItem is a container filesystem row that can be copied.
type FSEntryItem interface {
	Item
	FSEntry() podfs.FileEntry
}

// ContainerFSSource names the container directory a folder lists, for the
// copy dialog: the user sees where the files come from before the copy.
type ContainerFSSource struct {
	Context   string
	Namespace string
	Pod       string
	Container string
	Dir       string
	// Helper is set when the files are read through the debug helper.
	Helper bool
}

// TransferSource describes where the folder's entries come from.
func (f *PodContainerFSFolder) TransferSource() ContainerFSSource {
	src := ContainerFSSource{Context: f.Deps.ClusterKey.ContextName, Dir: f.dir}
	if f.session != nil {
		src.Namespace = f.session.spec.Namespace
		src.Pod = f.session.spec.Pod
		src.Container = f.session.spec.Container
		src.Helper = f.session.HelperUsed()
	}
	return src
}

// ErrHelperConsentNeeded means a copy needs a debug helper the user has not
// agreed to yet. The copy never starts one itself; the folder asks.
var ErrHelperConsentNeeded = errors.New("this container needs a debug helper; open the folder and start the helper first")

// TransferSession returns the folder's filesystem session for a copy. It
// reuses the session the listing opened, including a debug helper the
// user agreed to, and never adds a helper without that consent.
func (f *PodContainerFSFolder) TransferSession(ctx context.Context) (podfs.ExecSession, error) {
	if f.session == nil {
		return nil, fmt.Errorf("pod filesystem unavailable")
	}
	sess, err := f.session.Session(ctx)
	switch {
	case errors.Is(err, podfs.ErrHelperNeeded):
		return nil, ErrHelperConsentNeeded
	case err != nil:
		// Name the missing tool (no shell, no stat) instead of a raw exec error.
		return nil, errors.New(describeFSError("Open filesystem", err))
	}
	return sess, nil
}

// InvalidateSession drops the filesystem session, for example after a
// canceled copy left a command half read.
func (f *PodContainerFSFolder) InvalidateSession() {
	if f.session != nil {
		f.session.Invalidate()
	}
}

// UploadCommand is the kubectl cp command that copies the local path src
// into the folder's directory as name, for the confirmation dialog. Through
// the debug helper it names the helper container and the target's files
// under podfs.HelperRoot, which is where kc writes them.
func (f *PodContainerFSFolder) UploadCommand(src, name string) string {
	if f.session == nil {
		return ""
	}
	spec := f.session.spec
	container, dir := f.session.execTarget(f.dir)
	return kubectlCommand(f.Deps, "cp", src, spec.Namespace+"/"+spec.Pod+":"+path.Join(dir, name), "-c", container)
}

// HasEntry reports whether the folder's last listing has name. It does not
// list again; the copy itself lists before it decides.
func (f *PodContainerFSFolder) HasEntry(ctx context.Context, name string) bool {
	_, ok := f.ItemByID(ctx, joinContainerPath(f.dir, name))
	return ok
}
