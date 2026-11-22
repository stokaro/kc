package ui

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	kccluster "github.com/sttts/kc/internal/cluster"
	"github.com/sttts/kc/internal/logmux"
	"github.com/sttts/kc/internal/models"
	"github.com/sttts/kc/pkg/appconfig"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	crcache "sigs.k8s.io/controller-runtime/pkg/cache"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
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

// startWorkloadLogs resolves which pods spec covers, registers a follower on
// the shared pod informer and returns the merged stream. The mux lives on
// ctx; open opens one container stream.
func startWorkloadLogs(ctx context.Context, cl *kccluster.Cluster, spec models.WorkloadLogsSpec, maxRequests int, open logmux.Opener) (*workloadLogsSource, error) {
	log := ctrllog.FromContext(ctx).WithName("workloadLogs")
	startCtx, cancel := context.WithTimeout(ctx, workloadLogsStartTimeout)
	defer cancel()

	var (
		match    logmux.PodMatcher
		selector string
		what     string
	)
	if spec.IsPod() {
		match = logmux.NameMatcher(spec.Namespace, spec.Pod)
		what = "pod " + spec.Pod
	} else {
		obj, err := cl.GetByGVR(startCtx, spec.Workload, spec.Namespace, spec.WorkloadName)
		if err != nil {
			return nil, err
		}
		sel, err := logmux.WorkloadSelector(obj)
		if err != nil {
			return nil, err
		}
		match = logmux.SelectorMatcher(spec.Namespace, sel)
		selector = sel.String()
		what = "selector " + selector
	}

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

	tail := spec.TailLines
	if tail <= 0 {
		tail = models.DefaultLogsTailLines
	}
	mux := logmux.New(ctx, open, logmux.Options{
		LogOptions: corev1.PodLogOptions{Follow: spec.Follow, TailLines: &tail},
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

	reader := logmux.NewReader(mux, func(l logmux.Line) string { return logmux.Format(l, true) })
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

// workloadLogsReadyMsg carries a started merged stream (or the failure to
// start it) back to the update goroutine, which opens the viewer.
type workloadLogsReadyMsg struct {
	item    models.Item
	title   string
	command string
	source  *workloadLogsSource
	err     error
}

// openWorkloadLogs starts the merged stream of a logs row in a tea.Cmd: it
// reads the workload from the cache and may wait for the informer.
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
		src, err := startWorkloadLogs(ctx, cl, spec, maxRequests, open)
		if err != nil {
			return workloadLogsReadyMsg{item: item, err: err}
		}
		return workloadLogsReadyMsg{
			item:    item,
			title:   workloadLogsTitle(spec),
			command: provider.WorkloadLogsCommand(spec, src.selector, maxRequests),
			source:  src,
		}
	}
}

func workloadLogsTitle(spec models.WorkloadLogsSpec) string {
	if spec.IsPod() {
		return fmt.Sprintf("logs:%s/%s/all-containers", spec.Namespace, spec.Pod)
	}
	return fmt.Sprintf("logs:%s/%s/%s", spec.Namespace, spec.Workload.Resource, spec.WorkloadName)
}

// showWorkloadLogs opens the logs viewer over a started merged stream. It
// mirrors openLogsViewer; the viewer reads the merged stream like a single
// container log.
func (a *App) showWorkloadLogs(msg workloadLogsReadyMsg) tea.Cmd {
	if msg.err != nil {
		if a.toastLogger != nil {
			return a.toastLogger.Errorf("Logs stream failed: %v", msg.err)
		}
		return nil
	}
	src := msg.source
	logsViewer := NewLogsViewer(msg.title, src, nil, a.viewerTheme())
	logsViewer.SetStyledLines(true)
	logsViewer.SetCommand(msg.command)
	logsViewer.SetWrapMode(a.cfg != nil && strings.EqualFold(a.cfg.Viewer.Mode, appconfig.ViewerModeWrap))
	logsViewer.SetOnOptions(func() tea.Cmd { return a.showViewerOptionsModal(logsViewer) })
	logsViewer.SetOnClose(func() tea.Cmd {
		a.hideModal()
		return nil
	})
	modal := NewModal(a.modalTitleFromItem(msg.item, msg.title), logsViewer)
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
