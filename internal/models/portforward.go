package models

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/sttts/kc/internal/portforward"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	gvrPods     = schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	gvrServices = schema.GroupVersionResource{Version: "v1", Resource: "services"}
)

// PortForwardTarget is what a row can forward to: a pod, a container of a
// pod, or a service.
type PortForwardTarget struct {
	Namespace string
	Kind      string // "pod" or "service"
	Name      string
	Container string // set for container rows
}

// PortForwardTargetFor reports what item forwards to, if anything: pod and
// service rows, and container rows under a pod.
func PortForwardTargetFor(item Item) (PortForwardTarget, bool) {
	switch it := item.(type) {
	case *ContainerItem:
		if it.pod == "" {
			return PortForwardTarget{}, false
		}
		return PortForwardTarget{Namespace: it.namespace, Kind: "pod", Name: it.pod, Container: it.ID()}, true
	case ObjectItem:
		switch it.GVR() {
		case gvrPods:
			return PortForwardTarget{Namespace: it.Namespace(), Kind: "pod", Name: it.Name()}, true
		case gvrServices:
			return PortForwardTarget{Namespace: it.Namespace(), Kind: "service", Name: it.Name()}, true
		}
	}
	return PortForwardTarget{}, false
}

// FolderDeps exposes the dependencies of a folder, so actions on its rows
// reach the folder's cluster and context.
func (b *BaseFolder) FolderDeps() Deps { return b.Deps }

// PortForwardPlan is a prepared forward: the target, the ports it offers
// and the value the dialog starts with.
type PortForwardPlan struct {
	deps    Deps
	Target  PortForwardTarget
	Ports   []portforward.PortOption
	Default string
	Address string
}

// PreparePortForward reads the target of item and its ports. It reads the
// pod or service through the shared cache; call it off the UI goroutine.
func PreparePortForward(ctx context.Context, folder Folder, item Item) (*PortForwardPlan, error) {
	target, ok := PortForwardTargetFor(item)
	if !ok {
		return nil, errors.New("port forwarding works on pods, containers and services")
	}
	df, ok := folder.(interface{ FolderDeps() Deps })
	if !ok {
		return nil, errors.New("port forwarding is not available here")
	}
	deps := df.FolderDeps()
	if deps.PortForwards == nil || deps.Cl == nil {
		return nil, errors.New("port forwarding is not available")
	}
	plan := &PortForwardPlan{deps: deps, Target: target, Address: portForwardAddress(deps)}
	switch target.Kind {
	case "pod":
		pod, err := getPod(ctx, deps, target.Namespace, target.Name)
		if err != nil {
			return nil, err
		}
		plan.Ports = portforward.PodPorts(pod, target.Container)
	case "service":
		svc, err := getService(ctx, deps, target.Namespace, target.Name)
		if err != nil {
			return nil, err
		}
		plan.Ports = portforward.ServicePorts(svc)
	}
	plan.Default = portforward.DefaultPair(plan.Ports)
	return plan, nil
}

func portForwardAddress(deps Deps) string {
	if deps.AppConfig != nil {
		if a := strings.TrimSpace(deps.AppConfig.PortForward.Address); a != "" {
			return a
		}
	}
	return portforward.DefaultAddress
}

// Registry returns the registry the forward goes into.
func (p *PortForwardPlan) Registry() *portforward.Registry { return p.deps.PortForwards }

// Runner returns the runner that carries the forward's traffic.
func (p *PortForwardPlan) Runner() portforward.Runner {
	return portforward.ClientRunner{Config: p.deps.Cl.GetConfig()}
}

// Subject names the target as kubectl does: "pod/web", "service/web".
func (p *PortForwardPlan) Subject() string { return p.Target.Kind + "/" + p.Target.Name }

// Command is the kubectl command equivalent to forwarding value.
func (p *PortForwardPlan) Command(value string) string {
	return portForwardCommand(p.deps, p.Target, strings.TrimSpace(value), p.Address)
}

func portForwardCommand(deps Deps, t PortForwardTarget, pair, address string) string {
	if pair == "" {
		pair = "<local>:<remote>"
	}
	args := []string{"port-forward", "-n", t.Namespace, t.Kind + "/" + t.Name, pair}
	// kubectl listens on localhost (both loopback addresses) by default.
	if address != "" && address != "localhost" {
		args = append(args, "--address", address)
	}
	return kubectlCommand(deps, args...)
}

// PortsHint lists the offered ports for the dialog.
func (p *PortForwardPlan) PortsHint() string {
	if len(p.Ports) == 0 {
		if p.Target.Kind == "service" {
			return "The service declares no TCP ports."
		}
		return "No container ports are declared; any port the container listens on works."
	}
	parts := make([]string, 0, len(p.Ports))
	for _, o := range p.Ports {
		parts = append(parts, o.String())
	}
	return "Ports: " + strings.Join(parts, ", ")
}

// Resolve turns the dialog value into a forward: for a service it picks the
// backing pod and maps the service port to the pod port, as kubectl does.
// It reads through the shared cache; call it off the UI goroutine.
func (p *PortForwardPlan) Resolve(ctx context.Context, value string) (portforward.Spec, error) {
	pair, err := portforward.ParsePair(value)
	if err != nil {
		return portforward.Spec{}, err
	}
	switch p.Target.Kind {
	case "pod":
		pod, err := getPod(ctx, p.deps, p.Target.Namespace, p.Target.Name)
		if err != nil {
			return portforward.Spec{}, err
		}
		return resolvePortForward(p.deps, p.Target, p.Address, value, pair, nil, pod, nil)
	case "service":
		svc, err := getService(ctx, p.deps, p.Target.Namespace, p.Target.Name)
		if err != nil {
			return portforward.Spec{}, err
		}
		pods, err := listPods(ctx, p.deps, p.Target.Namespace)
		if err != nil {
			return portforward.Spec{}, err
		}
		return resolvePortForward(p.deps, p.Target, p.Address, value, pair, svc, nil, pods)
	}
	return portforward.Spec{}, fmt.Errorf("cannot forward to %s", p.Subject())
}

// resolvePortForward is the cluster-free part of Resolve: pod is the target
// pod for a pod forward, svc and pods the service and the candidate pods
// for a service forward.
func resolvePortForward(deps Deps, t PortForwardTarget, address, value string, pair portforward.Pair, svc *corev1.Service, pod *corev1.Pod, pods []corev1.Pod) (portforward.Spec, error) {
	spec := portforward.Spec{
		Context:   deps.CtxName,
		Namespace: t.Namespace,
		Kind:      t.Kind,
		Name:      t.Name,
		Container: t.Container,
		Address:   address,
		LocalPort: pair.Local,
		Command:   portForwardCommand(deps, t, strings.TrimSpace(value), address),
	}
	switch t.Kind {
	case "pod":
		if err := portforward.CheckPodRunning(pod); err != nil {
			return portforward.Spec{}, err
		}
		remote, err := portforward.PodPort(pod, pair.Remote)
		if err != nil {
			return portforward.Spec{}, err
		}
		spec.Pod = pod.Name
		spec.RemotePort = remote
	case "service":
		backing, err := portforward.SelectServicePod(svc, pods)
		if err != nil {
			return portforward.Spec{}, err
		}
		svcPort, podPort, err := portforward.ServicePort(svc, backing, pair.Remote)
		if err != nil {
			return portforward.Spec{}, err
		}
		spec.Pod = backing.Name
		spec.ServicePort = svcPort
		spec.RemotePort = podPort
	default:
		return portforward.Spec{}, fmt.Errorf("cannot forward to %s/%s", t.Kind, t.Name)
	}
	if pair.SameLocal {
		spec.LocalPort = spec.RemotePort
	}
	return spec, nil
}

func getPod(ctx context.Context, deps Deps, namespace, name string) (*corev1.Pod, error) {
	obj, err := deps.Cl.GetByGVR(ctx, gvrPods, namespace, name)
	if err != nil {
		return nil, fmt.Errorf("reading pod %s/%s: %w", namespace, name, err)
	}
	var pod corev1.Pod
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &pod); err != nil {
		return nil, err
	}
	return &pod, nil
}

func getService(ctx context.Context, deps Deps, namespace, name string) (*corev1.Service, error) {
	obj, err := deps.Cl.GetByGVR(ctx, gvrServices, namespace, name)
	if err != nil {
		return nil, fmt.Errorf("reading service %s/%s: %w", namespace, name, err)
	}
	var svc corev1.Service
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &svc); err != nil {
		return nil, err
	}
	return &svc, nil
}

func listPods(ctx context.Context, deps Deps, namespace string) ([]corev1.Pod, error) {
	list, err := deps.Cl.ListByGVR(ctx, gvrPods, namespace)
	if err != nil {
		return nil, fmt.Errorf("listing pods in %s: %w", namespace, err)
	}
	pods := make([]corev1.Pod, 0, len(list.Items))
	for i := range list.Items {
		var pod corev1.Pod
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(list.Items[i].Object, &pod); err != nil {
			return nil, err
		}
		pods = append(pods, pod)
	}
	return pods, nil
}
