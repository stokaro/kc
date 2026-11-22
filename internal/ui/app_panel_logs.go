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
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	kccluster "github.com/stokaro/kc/internal/cluster"
	"github.com/stokaro/kc/internal/logmux"
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
	return logswidget.New(p.manifestWidgetDeps(), logswidget.Options{
		Source:   a.panelLogsSource(),
		Theme:    a.viewerTheme(),
		Wrap:     a.cfg != nil && strings.EqualFold(a.cfg.Viewer.Mode, appconfig.ViewerModeWrap),
		Settings: settings,
	})
}

// panelLogsSource captures the cluster on the UI goroutine; the returned
// Open runs in a tea.Cmd and does the requests.
func (a *App) panelLogsSource() logswidget.Source {
	return func(req logswidget.Request) logswidget.Open {
		cl, root := a.cl, a.ctx
		maxRequests := appconfig.DefaultLogsMaxRequests
		if a.cfg != nil {
			maxRequests = a.cfg.Viewer.Logs.EffectiveMaxRequests()
		}
		return func() (logswidget.Stream, error) {
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
			opts := spec.PodLogOptions()
			ctx, cancel := context.WithCancel(root)
			log := ctrllog.FromContext(root).WithName("panelLogs")
			log.Info("client stream logs", "gvr", logsPodsGVR.String(), "namespace", spec.Namespace, "pod", spec.Pod,
				"container", spec.Container, "tailLines", spec.EffectiveTailLines(), "sinceSeconds", spec.SinceSeconds,
				"timestamps", spec.Timestamps, "previous", spec.Previous, "follow", spec.Follow)
			body, err := clientset.CoreV1().Pods(spec.Namespace).GetLogs(spec.Pod, opts).Stream(ctx)
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
	src, err := startWorkloadLogs(ctx, cl, models.WorkloadLogsSpec{Namespace: spec.Namespace, Pod: spec.Pod}, spec, maxRequests, open, false)
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
