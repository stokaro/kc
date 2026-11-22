package models

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"charm.land/lipgloss/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/stokaro/kc/internal/podfs"
	table "github.com/stokaro/kc/internal/table"
)

const (
	fsSessionTimeout        = 30 * time.Second
	fsDefaultTimeout        = 10 * time.Second
	fsFileViewMaxBytes      = 512 * 1024
	fsHelperRetryInterval   = 1 * time.Second
	fsContainerReadyTimeout = 5 * time.Second
	fsUnavailableMessage    = "Pod filesystem browsing unavailable"
)

// PodContainerDetailFolder exposes container-level virtual entries (logs, root filesystem, etc.).
type PodContainerDetailFolder struct {
	*BaseFolder
	Namespace string
	Pod       string
	Container string
	Kind      containerKind

	session *containerSessionHandle
	// execTarget carries the container's stdin and tty settings from the
	// list that opened the folder; nil builds a target without them.
	execTarget *ExecTarget
}

func NewPodContainerDetailFolder(deps Deps, folderPath []string, namespace, pod, container string, kind containerKind) *PodContainerDetailFolder {
	base := NewBaseFolder(deps, []table.Column{{Title: " Name"}}, folderPath)
	handle := newContainerSessionHandle(deps.PodFSFactory, namespace, pod, container)
	folder := &PodContainerDetailFolder{
		BaseFolder: base,
		Namespace:  namespace,
		Pod:        pod,
		Container:  container,
		Kind:       kind,
		session:    handle,
	}
	// The pod's status decides whether a previous log exists, so the rows
	// follow the pod like the container list does.
	base.SetRowSource(newPodContainerRowSource(deps, namespace, pod, kind, folder.buildRows, base.markDirtyFromSource))
	return folder
}

func (f *PodContainerDetailFolder) buildRows(ctx context.Context) ([]table.Row, error) {
	rows := make([]table.Row, 0, 3)
	logSpec := LogsSpec{
		Namespace: f.Namespace,
		Pod:       f.Pod,
		Container: f.Container,
		Follow:    true,
		TailLines: DefaultLogsTailLines,
	}
	logPath := append(slices.Clone(f.Path()), "logs", "latest")
	logItem := NewContainerLogItem(f.Deps, "logs_latest", []string{"logs"}, logPath, logSpec)
	logItem.RowItem.details = "Stream container logs"
	rows = append(rows, logItem)

	if restarts := f.restartCount(ctx); restarts > 0 {
		prevSpec := logSpec
		prevSpec.Previous = true
		prevPath := append(slices.Clone(f.Path()), "logs", "previous")
		prevItem := NewContainerLogItem(f.Deps, "logs_previous", []string{"logs (previous)"}, prevPath, prevSpec)
		prevItem.RowItem.details = fmt.Sprintf("Logs of the previous instance (%d restarts)", restarts)
		rows = append(rows, prevItem)
	}
	shellTarget := containerExecTarget(f.Deps, f.Namespace, f.Pod, f.Container, f.Kind, false, false)
	if f.execTarget != nil {
		shellTarget = *f.execTarget
	}
	shellItem := NewContainerShellItem("shell", []string{"shell"}, append(slices.Clone(f.Path()), "shell"), shellTarget)
	shellItem.RowItem.details = "Open a shell (bash, else sh)"
	shellItem.SetViewCommand(shellTarget.Command(ExecShell))
	rows = append(rows, shellItem)

	rootPath := append(slices.Clone(f.Path()), "root")
	if f.session == nil {
		unavailable := NewSimpleItem("root_unavailable", []string{"/root"}, rootPath, DimStyle())
		unavailable.RowItem.details = fsUnavailableMessage
		rows = append(rows, unavailable)
		return rows, nil
	}
	rootItem := NewContainerSectionItem("root", []string{"/root"}, rootPath, WhiteStyle(), func() (Folder, error) {
		return NewPodContainerFSFolder(f.Deps, rootPath, "/", f.session, f.Kind == containerKindEphemeral), nil
	})
	if f.session != nil && f.session.HelperUsed() {
		rootItem.RowItem.details = "Browse container filesystem (debug helper)"
	} else {
		rootItem.RowItem.details = "Browse container filesystem"
	}
	rows = append(rows, rootItem)
	return rows, nil
}

// restartCount returns the container's restart count from the pod status.
// It returns 0 when the pod cannot be read: the previous log row is an
// extra, and the other rows do not depend on the pod.
func (f *PodContainerDetailFolder) restartCount(ctx context.Context) int32 {
	if f.Deps.Cl == nil {
		return 0
	}
	gvr := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}
	obj, err := f.Deps.Cl.GetByGVR(ctx, gvr, f.Namespace, f.Pod)
	if err != nil || obj == nil {
		return 0
	}
	var pod corev1.Pod
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &pod); err != nil {
		return 0
	}
	return containerRestartCount(&pod, f.Container, f.Kind)
}

// containerRestartCount returns the restart count of the named container in
// the status list that matches kind.
func containerRestartCount(pod *corev1.Pod, name string, kind containerKind) int32 {
	statuses := pod.Status.ContainerStatuses
	switch kind {
	case containerKindInit:
		statuses = pod.Status.InitContainerStatuses
	case containerKindEphemeral:
		statuses = pod.Status.EphemeralContainerStatuses
	}
	for i := range statuses {
		if statuses[i].Name == name {
			return statuses[i].RestartCount
		}
	}
	return 0
}

// PodContainerFSFolder lists filesystem entries for a container under a given directory.
type PodContainerFSFolder struct {
	*BaseFolder
	dir       string
	session   *containerSessionHandle
	ephemeral bool

	refreshMu    sync.Mutex
	refreshTimer *time.Timer

	// Background loading. Listing a directory can take several round trips
	// (exec, then the debug helper, then ls), far longer than the UI may
	// block, so populate never touches the network itself.
	loadMu  sync.Mutex
	loading bool
	fresh   bool        // rows holds a load result not yet shown
	rows    []table.Row // last load result
}

func NewPodContainerFSFolder(deps Deps, folderPath []string, dir string, session *containerSessionHandle, ephemeral bool) *PodContainerFSFolder {
	if dir == "" {
		dir = "/"
	}
	base := NewBaseFolder(deps, []table.Column{
		{Title: " Name"},
		{Title: " Type"},
		{Title: " Size"},
		{Title: " Modified"},
	}, folderPath)
	folder := &PodContainerFSFolder{
		BaseFolder: base,
		dir:        sanitizeContainerPath(dir),
		session:    session,
		ephemeral:  ephemeral,
	}
	base.SetPopulate(folder.buildRows)
	return folder
}

// fsLoadTimeout bounds one background load: session setup plus listing.
const fsLoadTimeout = fsSessionTimeout + fsDefaultTimeout

// buildRows serves the last load result and starts a new load in the
// background. A finished load marks the folder dirty, and the next call
// returns its rows. Retries scheduled by a load go through the same path.
func (f *PodContainerFSFolder) buildRows(context.Context) ([]table.Row, error) {
	f.loadMu.Lock()
	defer f.loadMu.Unlock()
	if f.fresh {
		f.fresh = false
		return f.rows, nil
	}
	if !f.loading {
		f.loading = true
		go f.load()
	}
	if f.rows != nil {
		return f.rows, nil
	}
	return f.loadingRows(), nil
}

func (f *PodContainerFSFolder) load() {
	ctx, cancel := context.WithTimeout(f.Deps.Ctx, fsLoadTimeout)
	defer cancel()
	rows, err := f.loadRows(ctx)
	if err != nil {
		item := NewSimpleItem("root_error", []string{"error"}, f.Path(), DimStyle())
		detail := describeFSError("List directory", err)
		item.RowItem.details = detail
		rows = []table.Row{item}
	}
	f.loadMu.Lock()
	f.rows, f.fresh, f.loading = rows, true, false
	f.loadMu.Unlock()
	f.markDirty()
}

func (f *PodContainerFSFolder) loadingRows() []table.Row {
	item := NewSimpleItem(f.helperPendingRowID(), []string{"loading...", "", "", ""}, f.Path(), DimStyle())
	item.RowItem.details = "Reading " + f.dir
	return []table.Row{item}
}

func (f *PodContainerFSFolder) loadRows(ctx context.Context) ([]table.Row, error) {
	if f.session == nil {
		item := NewSimpleItem("root_unavailable", []string{"unavailable"}, f.Path(), DimStyle())
		item.RowItem.details = fsUnavailableMessage
		item.WithViewContent(errorViewContent("Pod filesystem unavailable", fsUnavailableMessage))
		return []table.Row{item}, nil
	}
	if ready, status, detail := f.containerReady(ctx); !ready {
		f.scheduleRefresh(fsHelperRetryInterval)
		return f.containerProgressRows(status, detail), nil
	}
	sessionCtx, cancelSession := context.WithTimeout(ctx, fsSessionTimeout)
	defer cancelSession()
	sess, err := f.session.Session(sessionCtx)
	if err != nil && errors.Is(err, podfs.ErrHelperNeeded) {
		return f.helperConsentRows(), nil
	}
	if err != nil {
		log := ctrllog.FromContext(sessionCtx).WithName("podfs_folder").
			WithValues("namespace", f.session.spec.Namespace, "pod", f.session.spec.Pod, "container", f.session.spec.Container)
		if isTransientSessionError(err) || isContextError(err) {
			log.Info("Session not ready; waiting for debug helper", "err", err)
			f.session.Invalidate()
			f.scheduleRefresh(fsHelperRetryInterval)
			return f.helperProgressRows(err), nil
		}
		log.Error(err, "Session establishment failed")
		item := NewSimpleItem("root_error", []string{"error"}, f.Path(), DimStyle())
		detail := describeFSError("Open filesystem", err)
		item.RowItem.details = detail
		item.WithViewContent(errorViewContent("Pod filesystem", detail))
		return []table.Row{item}, nil
	}
	listCtx, cancelList := context.WithTimeout(ctx, fsDefaultTimeout)
	defer cancelList()
	entries, err := sess.List(listCtx, f.dir)
	if err != nil && isTransientSessionError(err) {
		f.session.Invalidate()
		if sess, err = f.session.Session(sessionCtx); err == nil {
			entries, err = sess.List(listCtx, f.dir)
		}
	}
	if err != nil {
		log := ctrllog.FromContext(listCtx).WithName("podfs_folder").
			WithValues("namespace", f.session.spec.Namespace, "pod", f.session.spec.Pod, "container", f.session.spec.Container, "dir", f.dir)
		if isTransientSessionError(err) || isContextError(err) {
			log.Info("Filesystem helper still starting; retry scheduled", "err", err)
			f.session.Invalidate()
			f.scheduleRefresh(fsHelperRetryInterval)
			return f.helperProgressRows(err), nil
		}
		log.Error(err, "List failed")
		item := NewSimpleItem("root_error", []string{"error"}, f.Path(), DimStyle())
		detail := describeFSError("List directory", err)
		item.RowItem.details = detail
		item.WithViewContent(errorViewContent("Pod filesystem", detail))
		return []table.Row{item}, nil
	}
	rows := make([]table.Row, 0, len(entries))
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Type == entries[j].Type {
			return entries[i].Name < entries[j].Name
		}
		if entries[i].Type == podfs.EntryTypeDir {
			return true
		}
		if entries[j].Type == podfs.EntryTypeDir {
			return false
		}
		return entries[i].Name < entries[j].Name
	})
	for _, entry := range entries {
		rowPath := append(slices.Clone(f.Path()), entry.Name)
		displaySize := formatBytes(entry.Size)
		mod := formatModTime(entry.UpdatedAt)
		cells := []string{entry.Name, string(entry.Type), displaySize, mod}
		entryPath := joinContainerPath(f.dir, entry.Name)
		switch entry.Type {
		case podfs.EntryTypeDir:
			item := NewPodFSDirItem(entryPath, cells, rowPath, WhiteStyle(), func(nextDir string) func() (Folder, error) {
				return func() (Folder, error) {
					return NewPodContainerFSFolder(f.Deps, rowPath, nextDir, f.session, f.ephemeral), nil
				}
			}(entryPath))
			item.RowItem.details = fmt.Sprintf("Directory (%s)", entryPath)
			item.entry = entry
			rows = append(rows, item)
		case podfs.EntryTypeSymlink:
			fileItem := newPodFSFileItem(entryPath, cells, rowPath, entry, f)
			fileItem.RowItem.details = fmt.Sprintf("Symlink → %s", entry.Target)
			rows = append(rows, fileItem)
		default:
			fileItem := newPodFSFileItem(entryPath, cells, rowPath, entry, f)
			fileItem.RowItem.details = fileSizeDetails(entry.Size)
			rows = append(rows, fileItem)
		}
	}
	return rows, nil
}

func (f *PodContainerFSFolder) containerReady(ctx context.Context) (ready bool, state, detail string) {
	if f == nil || !f.ephemeral || f.session == nil || f.Deps.Cl == nil {
		return true, "", ""
	}
	name := strings.TrimSpace(f.session.spec.Container)
	if name == "" {
		return true, "", ""
	}
	readyCtx, cancel := context.WithTimeout(ctx, fsContainerReadyTimeout)
	defer cancel()
	gvr := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}
	obj, err := f.Deps.Cl.GetByGVR(readyCtx, gvr, f.session.spec.Namespace, f.session.spec.Pod)
	if err != nil {
		log := ctrllog.FromContext(readyCtx).WithName("podfs_folder").
			WithValues("namespace", f.session.spec.Namespace, "pod", f.session.spec.Pod, "container", name)
		log.Error(err, "Fetch pod for ephemeral container readiness failed")
		return true, "", ""
	}
	if obj == nil {
		return false, "pending", fmt.Sprintf("Waiting for ephemeral container %s", name)
	}
	var pod corev1.Pod
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &pod); err != nil {
		log := ctrllog.FromContext(readyCtx).WithName("podfs_folder").
			WithValues("namespace", f.session.spec.Namespace, "pod", f.session.spec.Pod, "container", name)
		log.Error(err, "Decode pod for ephemeral container readiness failed")
		return true, "", ""
	}
	status := findEphemeralStatus(&pod, name)
	if status == nil {
		if ephemeralSpecExists(&pod, name) {
			return false, "creating", fmt.Sprintf("Creating ephemeral container %s", name)
		}
		return false, "pending", fmt.Sprintf("Waiting for ephemeral container %s", name)
	}
	switch {
	case status.State.Running != nil:
		return true, "", ""
	case status.State.Waiting != nil:
		detail := containerStateDetail("Waiting", status.State.Waiting.Reason, status.State.Waiting.Message)
		return false, "waiting", detail
	case status.State.Terminated != nil:
		detail := containerStateDetail("Terminated", status.State.Terminated.Reason, status.State.Terminated.Message)
		return false, "terminated", detail
	default:
		return false, "pending", fmt.Sprintf("Ephemeral container %s pending", name)
	}
}

func containerStateDetail(prefix, reason, message string) string {
	reason = strings.TrimSpace(reason)
	message = strings.TrimSpace(message)
	if reason == "" && message == "" {
		return prefix
	}
	if reason != "" && message != "" {
		return fmt.Sprintf("%s: %s - %s", prefix, reason, message)
	}
	if reason != "" {
		return fmt.Sprintf("%s: %s", prefix, reason)
	}
	return fmt.Sprintf("%s: %s", prefix, message)
}

func findEphemeralStatus(pod *corev1.Pod, name string) *corev1.ContainerStatus {
	if pod == nil {
		return nil
	}
	for i := range pod.Status.EphemeralContainerStatuses {
		if pod.Status.EphemeralContainerStatuses[i].Name == name {
			return &pod.Status.EphemeralContainerStatuses[i]
		}
	}
	return nil
}

func ephemeralSpecExists(pod *corev1.Pod, name string) bool {
	if pod == nil {
		return false
	}
	for i := range pod.Spec.EphemeralContainers {
		if pod.Spec.EphemeralContainers[i].Name == name {
			return true
		}
	}
	return false
}

func (f *PodContainerFSFolder) helperProgressRows(err error) []table.Row {
	status := "starting..."
	detail := "Preparing ephemeral debug helper container"
	if err != nil {
		msg := strings.TrimSpace(err.Error())
		if msg != "" {
			status = "retrying"
			detail = fmt.Sprintf("Waiting for debug helper: %s", msg)
		}
	}
	cells := []string{"debug helper", status, "", ""}
	item := NewSimpleItem(f.helperPendingRowID(), cells, f.Path(), DimStyle())
	item.RowItem.details = detail
	return []table.Row{item}
}

func (f *PodContainerFSFolder) containerProgressRows(status, detail string) []table.Row {
	label := "ephemeral container"
	if f != nil && f.session != nil && strings.TrimSpace(f.session.spec.Container) != "" {
		label = f.session.spec.Container
	}
	status = strings.TrimSpace(status)
	if status == "" {
		status = "pending"
	}
	if detail == "" {
		detail = fmt.Sprintf("Waiting for %s to be ready", label)
	}
	cells := []string{label, status, "", ""}
	item := NewSimpleItem(f.containerPendingRowID(), cells, f.Path(), DimStyle())
	item.RowItem.details = detail
	return []table.Row{item}
}

func (f *PodContainerFSFolder) helperPendingRowID() string {
	if f == nil {
		return "__podfs_helper_pending__"
	}
	clean := strings.Trim(strings.ReplaceAll(f.dir, "/", "_"), "_")
	if clean == "" {
		clean = "root"
	}
	return "__podfs_helper_pending__" + clean
}

func (f *PodContainerFSFolder) containerPendingRowID() string {
	if f == nil {
		return "__podfs_container_pending__"
	}
	clean := strings.Trim(strings.ReplaceAll(f.dir, "/", "_"), "_")
	if clean == "" {
		clean = "root"
	}
	return "__podfs_container_pending__" + clean
}

func (f *PodContainerFSFolder) scheduleRefresh(delay time.Duration) {
	if f == nil {
		return
	}
	if delay <= 0 {
		f.markDirty()
		return
	}
	f.refreshMu.Lock()
	if f.refreshTimer != nil {
		f.refreshMu.Unlock()
		return
	}
	ctx := f.Deps.Ctx
	f.refreshTimer = time.AfterFunc(delay, func() {
		select {
		case <-ctx.Done():
			// Stop refreshing when the app context is canceled.
		default:
			f.markDirty()
		}
		f.refreshMu.Lock()
		f.refreshTimer = nil
		f.refreshMu.Unlock()
	})
	f.refreshMu.Unlock()
}

type PodFSDirItem struct {
	*RowItem
	enter func() (Folder, error)
	entry podfs.FileEntry
}

// FSEntry returns the listing entry the row shows.
func (d *PodFSDirItem) FSEntry() podfs.FileEntry { return d.entry }

// PodFSFileItem is a file, symbolic link or special file of a container.
type PodFSFileItem struct {
	*SimpleItem
	entry podfs.FileEntry
}

// FSEntry returns the listing entry the row shows.
func (i *PodFSFileItem) FSEntry() podfs.FileEntry { return i.entry }

func NewPodFSDirItem(id string, cells, rowPath []string, style *lipgloss.Style, enter func() (Folder, error)) *PodFSDirItem {
	return &PodFSDirItem{RowItem: NewRowItem(id, cells, rowPath, style), enter: enter}
}

func (d *PodFSDirItem) Enter() (Folder, error) {
	if d.enter == nil {
		return nil, nil
	}
	return d.enter()
}

func newPodFSFileItem(fsPath string, cells, rowPath []string, entry podfs.FileEntry, folder *PodContainerFSFolder) *PodFSFileItem {
	size, target := entry.Size, entry.Target
	item := NewSimpleItem(fsPath, cells, rowPath, WhiteStyle())
	detailsSuffix := ""
	if target != "" {
		detailsSuffix = fmt.Sprintf(" → %s", target)
	}
	item.RowItem.details = fmt.Sprintf("%d bytes%s", size, detailsSuffix)
	item.WithViewContent(podFSFileViewContent(folder, fsPath, size))
	if folder != nil && folder.session != nil {
		spec := folder.session.spec
		container, p := folder.session.execTarget(fsPath)
		item.SetViewCommand(kubectlCommand(folder.Deps, "exec", spec.Pod, "-n", spec.Namespace, "-c", container, "--", "cat", p))
	}
	return &PodFSFileItem{SimpleItem: item, entry: entry}
}

func podFSFileViewContent(folder *PodContainerFSFolder, fsPath string, size int64) ViewContentFunc {
	return func() (string, string, string, string, string, error) {
		if folder == nil || folder.session == nil {
			return "", "", "", "", "", fmt.Errorf("pod filesystem unavailable")
		}
		limit := size
		if limit <= 0 || limit > fsFileViewMaxBytes {
			limit = fsFileViewMaxBytes
		}
		data, err := folder.readFileContent(folder.Deps.Ctx, fsPath, limit)
		if err != nil {
			return "", "", "", "", "", err
		}
		body := string(data)
		filename := path.Base(fsPath)
		return fsPath, body, "", "", filename, nil
	}
}

type containerSessionHandle struct {
	factory podfs.Factory
	spec    podfs.SessionSpec

	mu      sync.Mutex
	session podfs.ExecSession
	helper  bool
	// helperContainer is the debug helper the session runs in, if any.
	helperContainer string
	// allowHelper records the user's consent to add a debug helper.
	allowHelper bool
}

// approveHelper lets the next session add a debug helper.
func (h *containerSessionHandle) approveHelper() {
	h.mu.Lock()
	h.allowHelper = true
	h.mu.Unlock()
}

// execTarget returns the container kubectl exec must use and how a path in
// the target container is spelled there: inside the debug helper the
// target's files live under podfs.HelperRoot.
func (h *containerSessionHandle) execTarget(p string) (container, execPath string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.helperContainer != "" {
		return h.helperContainer, podfs.HelperPath(p)
	}
	return h.spec.Container, p
}

func newContainerSessionHandle(factory podfs.Factory, namespace, pod, container string) *containerSessionHandle {
	if factory == nil {
		return nil
	}
	handle := &containerSessionHandle{
		factory: factory,
		spec: podfs.SessionSpec{
			Namespace: namespace,
			Pod:       pod,
			Container: container,
		},
	}
	return handle
}

func (h *containerSessionHandle) Session(ctx context.Context) (podfs.ExecSession, error) {
	if h == nil {
		return nil, fmt.Errorf("pod filesystem unavailable")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.session != nil {
		return h.session, nil
	}
	spec := h.spec
	spec.AllowHelper = h.allowHelper
	session, err := h.factory.NewSession(ctx, spec)
	if err != nil {
		return nil, err
	}
	// The consent covers starting one helper. Once it exits after going
	// idle, starting the next one asks again.
	h.allowHelper = false
	if usage, ok := session.(podfs.HelperUsage); ok {
		h.helper = usage.HelperUsed()
	} else {
		h.helper = false
	}
	h.helperContainer = ""
	if hc, ok := session.(interface{ HelperContainer() string }); ok {
		h.helperContainer = hc.HelperContainer()
	}
	h.session = session
	return session, nil
}

func (h *containerSessionHandle) Close() error {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.session == nil {
		return nil
	}
	err := h.session.Close()
	h.session = nil
	h.helper = false
	h.helperContainer = ""
	return err
}

func (h *containerSessionHandle) Invalidate() {
	if h == nil {
		return
	}
	h.mu.Lock()
	if h.session != nil {
		_ = h.session.Close()
		h.session = nil
		h.helper = false
		h.helperContainer = ""
	}
	h.mu.Unlock()
}

func (h *containerSessionHandle) HelperUsed() bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.helper
}

// fileSizeDetails is the info line of a file: the exact size, and the
// rounded one the Size column shows once that has a larger unit.
func fileSizeDetails(size int64) string {
	if size < 1024 {
		return fmt.Sprintf("%d bytes", size)
	}
	return fmt.Sprintf("%s (%d bytes)", formatBytes(size), size)
}

func formatBytes(size int64) string {
	if size < 1024 {
		return fmt.Sprintf("%d B", size)
	}
	const unit = 1024
	div, exp := int64(unit), 0
	for n := size / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	value := float64(size) / float64(div)
	return fmt.Sprintf("%.1f %ciB", value, "KMGTPE"[exp])
}

func formatModTime(ts time.Time) string {
	if ts.IsZero() {
		return ""
	}
	return ts.Local().Format("2006-01-02 15:04")
}

func sanitizeContainerPath(p string) string {
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return path.Clean(p)
}

func joinContainerPath(base, name string) string {
	base = sanitizeContainerPath(base)
	return sanitizeContainerPath(path.Join(base, name))
}

func describeFSError(action string, err error) string {
	if err == nil {
		return ""
	}
	var missing podfs.MissingCommandError
	switch {
	case errors.Is(err, podfs.ErrShellMissing):
		return "Filesystem browsing unavailable: container image lacks /bin/sh."
	case errors.As(err, &missing):
		return fmt.Sprintf("Filesystem browsing unavailable: missing required command %s", missing.Command)
	default:
		return fmt.Sprintf("%s failed: %v", action, err)
	}
}

func errorViewContent(title, detail string) ViewContentFunc {
	return func() (string, string, string, string, string, error) {
		return title, detail + "\n", "", "text/plain", "error.txt", nil
	}
}

func (f *PodContainerFSFolder) readFileContent(ctx context.Context, fsPath string, limit int64) ([]byte, error) {
	if f.session == nil {
		return nil, fmt.Errorf("pod filesystem unavailable")
	}
	for range 2 {
		sessionCtx, cancelSession := context.WithTimeout(ctx, fsSessionTimeout)
		sess, err := f.session.Session(sessionCtx)
		if err != nil {
			cancelSession()
			return nil, err
		}
		readCtx, cancelRead := context.WithTimeout(ctx, fsDefaultTimeout)
		reader, err := sess.ReadFile(readCtx, fsPath, limit)
		if err != nil {
			cancelRead()
			cancelSession()
			if isTransientSessionError(err) || isContextError(err) {
				f.session.Invalidate()
				continue
			}
			return nil, err
		}
		data, readErr := io.ReadAll(reader)
		reader.Close()
		cancelRead()
		cancelSession()
		if readErr != nil {
			if isTransientSessionError(readErr) || isContextError(readErr) {
				f.session.Invalidate()
				continue
			}
			return nil, readErr
		}
		return data, nil
	}
	return nil, fmt.Errorf("failed to read %s: transient errors", fsPath)
}

func isTransientSessionError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.ErrClosedPipe) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "read/write on closed pipe") || strings.Contains(msg, "connection reset by peer")
}

func isContextError(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// helperConsentRows offers to start a debug helper for a container without
// a shell. Starting one changes the pod, so it is a confirmed action.
func (f *PodContainerFSFolder) helperConsentRows() []table.Row {
	if f.Deps.ReadOnly {
		// Read-only mode: say why the container cannot be browsed, and offer
		// nothing to confirm. The UI refuses confirmed actions as well.
		item := NewSimpleItem("helper_read_only", []string{"Debug helper disabled (read-only mode)", "", "", ""}, f.Path(), DimStyle())
		item.RowItem.details = "The container has no shell, and read-only mode does not add a debug helper to the pod"
		return []table.Row{item}
	}
	item := &helperStartItem{
		SimpleItem: NewSimpleItem("start_helper", []string{"Start a debug helper...", "", "", ""}, f.Path(), WhiteStyle()),
		folder:     f,
	}
	item.RowItem.details = "The container has no shell; Enter starts a debug helper to browse it"
	return []table.Row{item}
}

// errReadOnlyHelper refuses the debug helper in read-only mode.
var errReadOnlyHelper = errors.New("read-only mode: the debug helper is disabled")

// helperStartItem asks for consent to add a debug helper to the pod.
type helperStartItem struct {
	*SimpleItem
	folder *PodContainerFSFolder
}

func (i *helperStartItem) Confirmation() Confirmation {
	spec := i.folder.session.spec
	return Confirmation{
		Title: "Start a debug helper?",
		Details: [][2]string{
			{"Context", i.folder.Deps.ClusterKey.ContextName},
			{"Namespace", spec.Namespace},
			{"Pod", spec.Pod},
			{"Container", spec.Container},
		},
		Notes: []string{
			"Adds an ephemeral busybox container with",
			"CAP_SYS_PTRACE to the pod. It exits after a",
			"minute without use but stays in the pod spec",
			"until the pod is recreated. kc asks again",
			"before it adds another one.",
		},
		Action: "Start",
	}
}

func (i *helperStartItem) Confirm() error {
	if i.folder.Deps.ReadOnly {
		return errReadOnlyHelper
	}
	i.folder.session.approveHelper()
	i.folder.reload()
	return nil
}

// reload drops the shown rows and loads the directory again.
func (f *PodContainerFSFolder) reload() {
	f.loadMu.Lock()
	f.rows, f.fresh = nil, false
	start := !f.loading
	f.loading = true
	f.loadMu.Unlock()
	if start {
		go f.load()
	}
	f.markDirty()
}
