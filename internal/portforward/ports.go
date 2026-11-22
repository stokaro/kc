// Package portforward runs kubectl-style port forwards inside kc and keeps
// track of the active ones.
package portforward

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	kubectlutil "k8s.io/kubectl/pkg/util"
	"k8s.io/kubectl/pkg/util/podutils"
)

// PortOption is a port the dialog offers: a container port of a pod, or a
// port of a service.
type PortOption struct {
	Name      string // port name, may be empty
	Port      int32
	Container string // container that declares the port (pods only)
}

// String renders the option for the dialog: "80/http (nginx)".
func (o PortOption) String() string {
	s := strconv.Itoa(int(o.Port))
	if o.Name != "" {
		s += "/" + o.Name
	}
	if o.Container != "" {
		s += " (" + o.Container + ")"
	}
	return s
}

// PodPorts lists the TCP container ports of a pod. With a container name
// only that container's ports are listed; init containers count, since a
// sidecar can serve.
func PodPorts(pod *corev1.Pod, container string) []PortOption {
	var out []PortOption
	add := func(c corev1.Container) {
		if container != "" && c.Name != container {
			return
		}
		for _, p := range c.Ports {
			if p.Protocol != "" && p.Protocol != corev1.ProtocolTCP {
				continue
			}
			out = append(out, PortOption{Name: p.Name, Port: p.ContainerPort, Container: c.Name})
		}
	}
	for _, c := range pod.Spec.Containers {
		add(c)
	}
	for _, c := range pod.Spec.InitContainers {
		add(c)
	}
	return out
}

// ServicePorts lists the TCP ports of a service.
func ServicePorts(svc *corev1.Service) []PortOption {
	var out []PortOption
	for _, p := range svc.Spec.Ports {
		if p.Protocol != "" && p.Protocol != corev1.ProtocolTCP {
			continue
		}
		out = append(out, PortOption{Name: p.Name, Port: p.Port})
	}
	return out
}

// DefaultPair is the dialog's prefilled value for the first offered port:
// the same number locally and remotely, as kubectl does for "8080".
func DefaultPair(opts []PortOption) string {
	if len(opts) == 0 {
		return ""
	}
	p := strconv.Itoa(int(opts[0].Port))
	return p + ":" + p
}

// Pair is a parsed "local:remote" value. Local 0 lets the system pick a
// free port. Remote is a number or a port name. SameLocal is set for a
// bare port name ("http"): the local port is the resolved remote port,
// as with kubectl.
type Pair struct {
	Local     int32
	Remote    string
	SameLocal bool
}

// ParsePair parses the dialog value the way kubectl reads one port
// argument: "8080:80", "8080" (same port on both sides) or ":80" (any
// local port). Only one pair is accepted.
func ParsePair(s string) (Pair, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Pair{}, errors.New("enter a port, for example 8080:80")
	}
	if strings.ContainsAny(s, " ,") {
		return Pair{}, errors.New("enter one port pair, for example 8080:80")
	}
	local, remote, found := strings.Cut(s, ":")
	if !found {
		remote = local
	}
	if remote == "" {
		return Pair{}, errors.New("the remote port is empty")
	}
	if strings.Contains(remote, ":") {
		return Pair{}, fmt.Errorf("invalid port pair %q", s)
	}
	if n, err := strconv.Atoi(remote); err == nil {
		if n < 1 || n > 65535 {
			return Pair{}, fmt.Errorf("remote port %d is out of range 1-65535", n)
		}
	} else if !validPortName(remote) {
		return Pair{}, fmt.Errorf("remote port %q is neither a number nor a port name", remote)
	}
	var lp int32
	switch local {
	case "":
		lp = 0
	default:
		n, err := strconv.Atoi(local)
		if err != nil {
			if !found {
				// "http" alone: a named remote port, the same port locally.
				return Pair{Remote: remote, SameLocal: true}, nil
			}
			return Pair{}, fmt.Errorf("local port %q is not a number", local)
		}
		if n < 0 || n > 65535 {
			return Pair{}, fmt.Errorf("local port %d is out of range 0-65535", n)
		}
		lp = int32(n)
	}
	return Pair{Local: lp, Remote: remote}, nil
}

// validPortName follows IANA_SVC_NAME loosely: letters, digits and '-',
// at least one letter, at most 15 characters.
func validPortName(s string) bool {
	if len(s) == 0 || len(s) > 15 {
		return false
	}
	letter := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
			letter = true
		case r >= '0' && r <= '9', r == '-':
		default:
			return false
		}
	}
	return letter
}

// PodPort resolves the remote side of a pod forward: a number is used as
// is, a name is looked up in the pod's container ports.
func PodPort(pod *corev1.Pod, remote string) (int32, error) {
	if n, err := strconv.Atoi(remote); err == nil {
		return int32(n), nil
	}
	return kubectlutil.LookupContainerPortNumberByName(*pod, remote)
}

// ServicePort resolves the remote side of a service forward like kubectl:
// the service port (number or name) maps to its target port on the pod,
// and a named target port is looked up in the pod. It returns the service
// port and the pod port.
func ServicePort(svc *corev1.Service, pod *corev1.Pod, remote string) (svcPort, podPort int32, err error) {
	if n, convErr := strconv.Atoi(remote); convErr == nil {
		svcPort = int32(n)
	} else {
		svcPort, err = kubectlutil.LookupServicePortNumberByName(*svc, remote)
		if err != nil {
			return 0, 0, err
		}
	}
	podPort, err = kubectlutil.LookupContainerPortNumberByServicePort(*svc, *pod, svcPort)
	if err != nil {
		return 0, 0, err
	}
	return svcPort, podPort, nil
}

// SelectServicePod picks the pod a service forward goes to, as kubectl
// does: pods matching the selector, the most available one first (ready,
// running, scheduled, oldest). The pod must be running and not deleting.
func SelectServicePod(svc *corev1.Service, pods []corev1.Pod) (*corev1.Pod, error) {
	if len(svc.Spec.Selector) == 0 {
		return nil, fmt.Errorf("service %s has no selector, so it has no pods to forward to", svc.Name)
	}
	sel := labels.SelectorFromSet(svc.Spec.Selector)
	var matched []*corev1.Pod
	for i := range pods {
		p := &pods[i]
		if p.Namespace != svc.Namespace || !sel.Matches(labels.Set(p.Labels)) {
			continue
		}
		matched = append(matched, p)
	}
	if len(matched) == 0 {
		return nil, fmt.Errorf("no pods match the selector of service %s", svc.Name)
	}
	sort.Sort(sort.Reverse(podutils.ActivePods(matched)))
	for _, p := range matched {
		if p.DeletionTimestamp == nil && p.Status.Phase == corev1.PodRunning {
			return p, nil
		}
	}
	return nil, fmt.Errorf("no running pod backs service %s", svc.Name)
}

// CheckPodRunning refuses a pod that cannot serve a forward, with kubectl's
// reason.
func CheckPodRunning(pod *corev1.Pod) error {
	if pod.Status.Phase != corev1.PodRunning {
		return fmt.Errorf("pod %s is not running (phase %s)", pod.Name, pod.Status.Phase)
	}
	return nil
}
