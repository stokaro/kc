package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	kccluster "github.com/stokaro/kc/internal/cluster"
	"github.com/stokaro/kc/internal/logmux"
	"github.com/stokaro/kc/internal/logview"
	models "github.com/stokaro/kc/internal/models"
	logswidget "github.com/stokaro/kc/internal/ui/panelcontent/logs"
	"github.com/stokaro/kc/pkg/appconfig"
)

var logsPodsGVR = schema.GroupVersionResource{Version: "v1", Resource: "pods"}

func (a *App) newPanelLogsWidget(p *Panel) PanelWidget {
	var settings logswidget.Settings
	if a.cfg != nil {
		defaults := logsSpecWithDefaults(models.LogsSpec{}, a.cfg.Viewer.Logs)
		settings = logswidget.Settings{
			TailLines:    defaults.TailLines,
			SinceSeconds: defaults.SinceSeconds,
			Timestamps:   defaults.Timestamps,
		}
	}
	w := logswidget.New(p.manifestWidgetDeps(), logswidget.Options{
		Source:   a.panelLogsSource(),
		Theme:    a.viewerTheme(),
		Wrap:     a.cfg != nil && strings.EqualFold(a.cfg.Viewer.Mode, appconfig.ViewerModeWrap),
		Settings: settings,
		Limits:   a.logLimits(),
	})
	_ = w.SetSpan(a.logsSpan())
	return w
}

// logLimits are the limits of what one logs viewer or Logs panel keeps,
// from viewer.logs.maxLines and viewer.logs.maxMemoryMiB.
func (a *App) logLimits() logview.Limits {
	var cfg appconfig.LogsViewerConfig
	if a.cfg != nil {
		cfg = a.cfg.Viewer.Logs
	}
	return logview.Limits{MaxRecords: cfg.EffectiveMaxLines(), MaxBytes: cfg.EffectiveMaxMemoryBytes()}
}

// panelLogsSource resolves the cluster of the selection (req.Source) on
// the UI goroutine; the returned Open runs in a tea.Cmd and does the
// requests. The stream holds the connection until it is canceled, so the
// other panel may switch its context while the stream runs.
//
//nolint:gocognit // the stream opener runs in two nested closures; each branch is one way to open the logs (all containers, the default container, one container)
func (a *App) panelLogsSource() logswidget.Source {
	return func(req logswidget.Request) logswidget.Open {
		conn := a.connByID(req.Source)
		// A selection of a connection nobody holds any more was made
		// before its panel switched the context.
		gone := conn == nil && req.Source != ""
		if conn == nil && !gone {
			conn = a.activeConn()
		}
		cl, root := conn.cluster(), a.ctx
		maxRequests := appconfig.DefaultLogsMaxRequests
		if a.cfg != nil {
			maxRequests = a.cfg.Viewer.Logs.EffectiveMaxRequests()
		}
		// The hold is taken here, on the UI goroutine, while the panel
		// still holds conn; the widget always runs the returned Open.
		release := conn.hold()
		return func() (stream logswidget.Stream, err error) {
			if gone {
				return logswidget.Stream{}, errContextChanged
			}
			defer func() {
				if err != nil {
					release()
					return
				}
				cancel := stream.Cancel
				stream.Cancel = func() {
					if cancel != nil {
						cancel()
					}
					release()
				}
			}()
			if cl == nil {
				return logswidget.Stream{}, errors.New("no active cluster")
			}
			spec := req.Spec
			// The pod comes from the shared cache. Its containers fill the
			// F2 choice; only the default container needs it.
			pod, podErr := panelLogsPod(root, cl, spec)
			var containers []string
			if podErr == nil {
				containers = logswidget.ContainerNames(pod)
			}
			if req.AllContainers {
				open, err := clientLogOpener(cl.GetConfig())
				if err != nil {
					return logswidget.Stream{}, err
				}
				return openPanelAllContainers(root, cl, spec, maxRequests, open, containers)
			}
			if spec.Container == "" {
				if podErr != nil {
					return logswidget.Stream{}, podErr
				}
				spec.Container = logswidget.DefaultContainer(pod)
				if spec.Container == "" {
					return logswidget.Stream{}, fmt.Errorf("pod %s/%s has no containers", spec.Namespace, spec.Pod)
				}
			}
			clientset, err := kubernetes.NewForConfig(rest.AddUserAgent(rest.CopyConfig(cl.GetConfig()), "kc-log-panel"))
			if err != nil {
				return logswidget.Stream{}, err
			}
			ctx, cancel := context.WithCancel(root)
			body, err := openResumingLogs(ctx, cl, clientset, spec, "panelLogs")
			if err != nil {
				cancel()
				return logswidget.Stream{}, err
			}
			return logswidget.Stream{Spec: spec, Containers: containers, Body: body, Cancel: cancel}, nil
		}
	}
}

// panelLogsPod reads the pod of spec from the shared cache.
func panelLogsPod(root context.Context, cl *kccluster.Cluster, spec models.LogsSpec) (*corev1.Pod, error) {
	ctx, cancel := context.WithTimeout(root, requestTimeout)
	defer cancel()
	obj, err := cl.GetByGVR(ctx, logsPodsGVR, spec.Namespace, spec.Pod)
	if err != nil {
		return nil, err
	}
	var pod corev1.Pod
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &pod); err != nil {
		return nil, err
	}
	return &pod, nil
}

// openPanelAllContainers merges the logs of every started container of the
// pod through logmux, as the pod's all-containers logs row does in the
// fullscreen viewer. The line prefix is plain: the panel viewer shows text.
// open opens one container stream.
func openPanelAllContainers(root context.Context, cl *kccluster.Cluster, spec models.LogsSpec, maxRequests int, open logmux.Opener, containers []string) (logswidget.Stream, error) {
	spec.Container = ""
	ctx, cancel := context.WithCancel(root)
	src, err := startWorkloadLogs(ctx, cl, models.WorkloadLogsSpec{Namespace: spec.Namespace, Pod: spec.Pod}, spec, maxRequests, open, false, nil)
	if err != nil {
		cancel()
		return logswidget.Stream{}, err
	}
	return logswidget.Stream{Spec: spec, Containers: containers, Body: src, Cancel: cancel}, nil
}

// followOtherPanel starts a panel that just entered Describe, Manifest or
// Logs mode on the other panel's selection. Panel.SetMode gives the new
// widget the panel's own cursor row, and the mode chooser replays the
// other selection itself, but the C-1 / C-2 cycle keys and the F9 menu do
// not. Info mode resolves the other panel on its own.
func (a *App) followOtherPanel(ctx context.Context, panel *Panel) tea.Cmd {
	if panel == nil {
		return nil
	}
	switch panel.Mode() {
	case PanelModeDescribe, PanelModeManifest, PanelModeLogs:
	default:
		return nil
	}
	idx, ok := a.panelIndexFor(panel)
	if !ok {
		return nil
	}
	other := a.panelByIndex(1 - idx)
	if other == nil {
		return nil
	}
	sel := other.CurrentSelection(ctx)
	if item, ok := other.SelectedNavItem(ctx); ok {
		sel.Item = item
	}
	sel.Force = true
	return panel.NotifySelection(ctx, sel)
}
