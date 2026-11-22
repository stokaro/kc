package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	models "github.com/sttts/kc/internal/models"
	logswidget "github.com/sttts/kc/internal/ui/panelcontent/logs"
	"github.com/sttts/kc/pkg/appconfig"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

var logsPodsGVR = schema.GroupVersionResource{Version: "v1", Resource: "pods"}

func (a *App) newPanelLogsWidget(p *Panel) PanelWidget {
	return logswidget.New(p.manifestWidgetDeps(), logswidget.Options{
		Source: a.panelLogsSource(),
		Theme:  a.viewerTheme(),
		Wrap:   a.cfg != nil && strings.EqualFold(a.cfg.Viewer.Mode, appconfig.ViewerModeWrap),
	})
}

// panelLogsSource captures the cluster on the UI goroutine; the returned
// Open runs in a tea.Cmd and does the requests.
func (a *App) panelLogsSource() logswidget.Source {
	return func(spec models.LogsSpec) logswidget.Open {
		cl, root := a.cl, a.ctx
		if a.cfg != nil {
			spec = logsSpecWithDefaults(spec, a.cfg.Viewer.Logs)
		}
		return func() (logswidget.Stream, error) {
			if cl == nil {
				return logswidget.Stream{}, errors.New("no active cluster")
			}
			log := ctrllog.FromContext(root).WithName("panelLogs")
			if spec.Container == "" {
				// The pod comes from the shared cache.
				ctx, cancel := context.WithTimeout(root, requestTimeout)
				obj, err := cl.GetByGVR(ctx, logsPodsGVR, spec.Namespace, spec.Pod)
				cancel()
				if err != nil {
					return logswidget.Stream{}, err
				}
				var pod corev1.Pod
				if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &pod); err != nil {
					return logswidget.Stream{}, err
				}
				spec.Container = logswidget.DefaultContainer(&pod)
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
			log.Info("client stream logs", "gvr", logsPodsGVR.String(), "namespace", spec.Namespace, "pod", spec.Pod,
				"container", spec.Container, "tailLines", spec.EffectiveTailLines(), "sinceSeconds", spec.SinceSeconds,
				"timestamps", spec.Timestamps, "follow", spec.Follow)
			body, err := clientset.CoreV1().Pods(spec.Namespace).GetLogs(spec.Pod, opts).Stream(ctx)
			if err != nil {
				cancel()
				return logswidget.Stream{}, err
			}
			return logswidget.Stream{Spec: spec, Body: body, Cancel: cancel}, nil
		}
	}
}

// followOtherPanelLogs starts a panel that just entered Logs mode on the
// other panel's selection. The mode chooser replays the selection itself,
// but the C-1 / C-2 cycle keys do not.
func (a *App) followOtherPanelLogs(ctx context.Context, panel *Panel) tea.Cmd {
	if panel == nil || panel.Mode() != PanelModeLogs {
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
