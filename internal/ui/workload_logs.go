package ui

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	crcache "sigs.k8s.io/controller-runtime/pkg/cache"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	kccluster "github.com/stokaro/kc/internal/cluster"
	"github.com/stokaro/kc/internal/logmux"
	"github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/pkg/appconfig"
)

// workloadLogsStartTimeout bounds reading the workload and getting the pod
// informer from the shared cache.
const workloadLogsStartTimeout = 10 * time.Second

// workloadLogsSyncPoll is how often the source checks whether the initial
// pod list has been delivered, to report a selector that matches nothing.
const workloadLogsSyncPoll = 100 * time.Millisecond

// workloadLogsSource is the merged stream behind a workload logs viewer. It
// reads like a single container log; Close stops following pods and every
// stream.
type workloadLogsSource struct {
	*logmux.Reader
	mux      *logmux.Mux
	stop     func()
	selector string
}

// Close removes the pod event handler and closes the mux.
func (s *workloadLogsSource) Close() error {
	s.stop()
	return s.Reader.Close()
}

// workloadPods says which pods a merged log covers.
type workloadPods struct {
	match logmux.PodMatcher
	// selector is the workload's pod selector, "" for one pod.
	selector string
	// what names the pods for messages.
	what string
}

// resolveWorkloadPods reads the workload's selector from the shared cache.
func resolveWorkloadPods(ctx context.Context, cl *kccluster.Cluster, spec models.WorkloadLogsSpec) (workloadPods, error) {
	if spec.IsPod() {
		return workloadPods{match: logmux.NameMatcher(spec.Namespace, spec.Pod), what: "pod " + spec.Pod}, nil
	}
	obj, err := cl.GetByGVR(ctx, spec.Workload, spec.Namespace, spec.WorkloadName)
	if err != nil {
		return workloadPods{}, err
	}
	sel, err := logmux.WorkloadSelector(obj)
	if err != nil {
		return workloadPods{}, err
	}
	return workloadPods{
		match:    logmux.SelectorMatcher(spec.Namespace, sel),
		selector: sel.String(),
		what:     "selector " + sel.String(),
	}, nil
}

// startWorkloadLogs resolves which pods spec covers, registers a follower on
// the shared pod informer and returns the merged stream with the options of
// logs (its Container is ignored; the mux sets it per target). The mux lives
// on ctx; open opens one container stream. color selects the ANSI-colored
// line prefix of the fullscreen viewer over the plain one of the Logs panel.
func startWorkloadLogs(ctx context.Context, cl *kccluster.Cluster, spec models.WorkloadLogsSpec, logs models.LogsSpec, maxRequests int, open logmux.Opener, color bool) (*workloadLogsSource, error) {
	log := ctrllog.FromContext(ctx).WithName("workloadLogs")
	startCtx, cancel := context.WithTimeout(ctx, workloadLogsStartTimeout)
	defer cancel()

	pods, err := resolveWorkloadPods(startCtx, cl, spec)
	if err != nil {
		return nil, err
	}
	match, selector, what := pods.match, pods.selector, pods.what

	podGVR := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	gvk, err := cl.RESTMapper().KindFor(podGVR)
	if err != nil {
		return nil, err
	}
	podObj := &unstructured.Unstructured{}
	podObj.SetGroupVersionKind(gvk)
	informer, err := cl.GetCache().GetInformer(startCtx, podObj, crcache.BlockUntilSynced(false))
	if err != nil {
		return nil, fmt.Errorf("pod informer: %w", err)
	}

	logOpts := *logs.PodLogOptions()
	logOpts.Container = ""
	mux := logmux.New(ctx, open, logmux.Options{
		LogOptions: logOpts,
		MaxStreams: maxRequests,
	})
	follower := logmux.NewFollower(mux, match)
	reg, err := informer.AddEventHandler(follower)
	if err != nil {
		mux.Close()
		return nil, fmt.Errorf("pod informer handler: %w", err)
	}
	stop := func() {
		if err := informer.RemoveEventHandler(reg); err != nil {
			log.Error(err, "removing pod handler")
		}
	}
	log.V(1).Info("following pods", "namespace", spec.Namespace, "match", what)

	// Tell the user when the initial list has nothing to stream, rather than
	// showing an empty viewer.
	go func() {
		for !reg.HasSynced() {
			select {
			case <-mux.Done():
				return
			case <-time.After(workloadLogsSyncPoll):
			}
		}
		if follower.Pods() == 0 {
			mux.Notify("no pods match %s in %s yet; waiting for them", what, spec.Namespace)
		}
	}()

	reader := logmux.NewReader(mux, func(l logmux.Line) string { return logmux.Format(l, color) })
	return &workloadLogsSource{Reader: reader, mux: mux, stop: stop, selector: selector}, nil
}

// clientLogOpener opens container log streams with a clientset for cfg.
func clientLogOpener(cfg *rest.Config) (logmux.Opener, error) {
	clientset, err := kubernetes.NewForConfig(rest.AddUserAgent(rest.CopyConfig(cfg), "kc-log-viewer"))
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, t logmux.Target, opts *corev1.PodLogOptions) (io.ReadCloser, error) {
		var tail int64
		if opts.TailLines != nil {
			tail = *opts.TailLines
		}
		ctrllog.FromContext(ctx).WithName("workloadLogs").Info("client stream logs",
			"gvr", "v1/pods", "namespace", t.Namespace, "pod", t.Pod, "container", t.Container,
			"tailLines", tail, "follow", opts.Follow)
		return clientset.CoreV1().Pods(t.Namespace).GetLogs(t.Pod, opts).Stream(ctx)
	}, nil
}

// workloadLogsReadyMsg carries the resolved pods of a merged log (or the
// failure to resolve them) back to the update goroutine, which opens the
// viewer. The selector is resolved first so the command line can show it.
type workloadLogsReadyMsg struct {
	item        models.Item
	provider    models.WorkloadLogsProvider
	spec        models.WorkloadLogsSpec
	selector    string
	maxRequests int
	open        logmux.Opener
	err         error
}

// openWorkloadLogs resolves the pods of a logs row in a tea.Cmd: it reads
// the workload from the cache.
func (a *App) openWorkloadLogs(item models.Item, provider models.WorkloadLogsProvider) tea.Cmd {
	if a.cl == nil {
		if a.toastLogger != nil {
			a.enqueueCmd(a.toastLogger.Errorf("Logs unavailable: no active cluster"))
		}
		return nil
	}
	spec := provider.WorkloadLogsSpec()
	maxRequests := appconfig.DefaultLogsMaxRequests
	if a.cfg != nil {
		maxRequests = a.cfg.Viewer.Logs.EffectiveMaxRequests()
	}
	cl, ctx := a.cl, a.ctx
	return func() tea.Msg {
		open, err := clientLogOpener(cl.GetConfig())
		if err != nil {
			return workloadLogsReadyMsg{item: item, err: err}
		}
		resolveCtx, cancel := context.WithTimeout(ctx, workloadLogsStartTimeout)
		defer cancel()
		pods, err := resolveWorkloadPods(resolveCtx, cl, spec)
		if err != nil {
			return workloadLogsReadyMsg{item: item, err: err}
		}
		return workloadLogsReadyMsg{
			item:        item,
			provider:    provider,
			spec:        spec,
			selector:    pods.selector,
			maxRequests: maxRequests,
			open:        open,
		}
	}
}

func workloadLogsTitle(spec models.WorkloadLogsSpec) string {
	if spec.IsPod() {
		return fmt.Sprintf("logs:%s/%s/all-containers", spec.Namespace, spec.Pod)
	}
	return fmt.Sprintf("logs:%s/%s/%s", spec.Namespace, spec.Workload.Resource, spec.WorkloadName)
}

// showWorkloadLogs opens the logs viewer on a merged stream. It mirrors
// openLogsViewer: the viewer opens the stream in a tea.Cmd and reads it like
// a single container log, and every F2 change closes the merged stream and
// starts a new one with the new options.
func (a *App) showWorkloadLogs(msg workloadLogsReadyMsg) tea.Cmd {
	if msg.err != nil {
		if a.toastLogger != nil {
			return a.toastLogger.Errorf("Logs stream failed: %v", msg.err)
		}
		return nil
	}
	cl, parent := a.cl, a.ctx
	if cl == nil {
		return nil
	}
	spec, maxRequests, openContainer := msg.spec, msg.maxRequests, msg.open
	logs := spec.LogsSpec()
	logs.Follow = true
	if a.cfg != nil {
		logs = logsSpecWithDefaults(logs, a.cfg.Viewer.Logs)
	}
	open := func(ls models.LogsSpec) (io.ReadCloser, context.CancelFunc, error) {
		src, err := startWorkloadLogs(parent, cl, spec, ls, maxRequests, openContainer, true)
		if err != nil {
			return nil, nil, err
		}
		// Closing the source removes the pod handler and stops the mux.
		return src, nil, nil
	}
	title := workloadLogsTitle(spec)
	logsViewer := NewLogsViewer(title, logs, open, a.viewerTheme())
	logsViewer.SetStyledLines(true)
	provider, selector := msg.provider, msg.selector
	logsViewer.SetCommandBuilder(func(ls models.LogsSpec) string {
		return provider.WorkloadLogsCommand(spec, ls, selector, maxRequests)
	})
	logsViewer.SetWrapMode(a.cfg != nil && strings.EqualFold(a.cfg.Viewer.Mode, appconfig.ViewerModeWrap))
	logsViewer.SetOnOptions(func() tea.Cmd { return a.showViewerOptionsModal(logsViewer) })
	logsViewer.SetOnFilter(func() tea.Cmd { return a.showLogsFilterModal(logsViewer) })
	logsViewer.SetOnClose(func() tea.Cmd {
		a.hideModal()
		return nil
	})
	modal := NewModal(a.modalTitleFromItem(msg.item, title), logsViewer)
	modal.SetMode(ModalModeFullscreen)
	modal.SetDimensions(a.width, a.height)
	modal.SetCloseOnSingleEsc(false)
	modal.SetCloseOnEscTimeout(true)
	modal.SetOnClose(func() tea.Cmd {
		logsViewer.Close()
		return nil
	})
	a.modalManager.Register("logs_viewer", modal)
	a.showModal("logs_viewer")
	return logsViewer.Init()
}
