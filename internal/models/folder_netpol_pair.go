package models

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/stokaro/kc/internal/netpol"
	table "github.com/stokaro/kc/internal/table"
)

var (
	networkPolicyGVR    = schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "networkpolicies"}
	netpolNamespacesGVR = schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}
)

// netpolNote says what the evaluation does not prove.
const netpolNote = "Policy configuration only: kc evaluates networking.k8s.io/v1 NetworkPolicies as the API defines them. " +
	"It does not probe a connection, read plugin-specific policies (Calico, Cilium, AdminNetworkPolicy) or know whether the network plugin enforces NetworkPolicy at all."

// ErrNotPodPair means the two rows are not both pods.
var ErrNotPodPair = errors.New("put the cursor on a pod in each panel: the active panel's pod is the source, the other panel's the destination")

// PodRef names a pod by identity: a pod recreated under the same name is
// another pod.
type PodRef struct {
	Namespace, Name string
	UID             types.UID
}

// PodRefOf returns the pod an object row stands for.
func PodRefOf(item Item) (PodRef, bool) {
	obj, ok := item.(ObjectItem)
	if !ok || obj.GVR() != podGVR {
		return PodRef{}, false
	}
	ref := PodRef{Namespace: obj.Namespace(), Name: obj.Name()}
	if u, ok := item.(interface{ UID() types.UID }); ok {
		ref.UID = u.UID()
	}
	return ref, true
}

// NetworkPolicyPairFolder explains the NetworkPolicy decision between two
// pods, read once: the identities, policies and labels are those of the
// read time, and moving a cursor does not change it.
type NetworkPolicyPairFolder struct {
	*BaseFolder
	reader *scopedReader
	once   sync.Once
	rows   []table.Row
}

// LoadNetworkPolicyPair reads both pods, the NetworkPolicies of their
// namespaces and the labels of the source namespace, and evaluates the
// ports of the destination. It reads through the same per-type scope
// decision as related folders, so a namespace-only user reads in the
// namespace. It may block on the cache; call it off the UI thread.
func LoadNetworkPolicyPair(ctx context.Context, deps Deps, src, dst PodRef) *NetworkPolicyPairFolder {
	unscoped, release := deps.unscoped()
	reader := newScopedReader(depsResolver(unscoped))
	reader.releases = append(reader.releases, release)
	path := []string{"network-policy", src.Namespace + "/" + src.Name + " -> " + dst.Namespace + "/" + dst.Name}
	f := &NetworkPolicyPairFolder{reader: reader}
	f.BaseFolder = NewBaseFolder(unscoped, relatedColumns("Evidence"), path)
	in, problems := readNetpolPair(ctx, reader, src, dst)
	f.rows = netpolPairRows(unscoped, f.rowDeps, path, src, dst, in, problems, time.Now())
	f.SetPopulate(func(context.Context) ([]table.Row, error) { return f.rows, nil })
	ctrllog.FromContext(ctx).WithName("netpol").V(1).Info("network policy pair", "src", src.Namespace+"/"+src.Name, "dst", dst.Namespace+"/"+dst.Name, "problems", len(problems))
	return f
}

// StandalonePath tells the navigator that Path is the whole breadcrumb.
func (f *NetworkPolicyPairFolder) StandalonePath() bool { return true }

// Close gives up the caches the read acquired.
func (f *NetworkPolicyPairFolder) Close() { f.once.Do(f.reader.Close) }

func (f *NetworkPolicyPairFolder) rowDeps(ref RelatedRef) Deps {
	if sc, ok := f.reader.decided(ref.GVR, ref.Namespace); ok && sc.scope != scopeDenied && sc.deps.Cl != nil {
		return sc.deps
	}
	return f.Deps
}

// netpolProblem is a source that could not be read or a pod that changed.
type netpolProblem struct {
	code, text string
	fatal      bool // the pair cannot be evaluated at all
}

func readNetpolPair(ctx context.Context, r RelatedReader, src, dst PodRef) (netpol.Input, []netpolProblem) {
	var in netpol.Input
	var problems []netpolProblem
	readPod := func(ref PodRef, role string) (netpol.Pod, bool) {
		obj, err := r.GetByGVR(ctx, podGVR, ref.Namespace, ref.Name)
		if err != nil {
			problems = append(problems, netpolProblem{code: role + "-unreadable", fatal: true,
				text: fmt.Sprintf("The %s pod %s/%s could not be read: %s", role, ref.Namespace, ref.Name, readProblem(err))})
			return netpol.Pod{}, false
		}
		var p corev1.Pod
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &p); err != nil {
			problems = append(problems, netpolProblem{code: role + "-undecodable", fatal: true, text: err.Error()})
			return netpol.Pod{}, false
		}
		if ref.UID != "" && p.UID != ref.UID {
			problems = append(problems, netpolProblem{code: role + "-replaced", fatal: true,
				text: fmt.Sprintf("The %s pod %s/%s was replaced (UID %s, selected %s); select it again", role, ref.Namespace, ref.Name, p.UID, ref.UID)})
			return netpol.Pod{}, false
		}
		return netpol.PodFrom(&p), true
	}
	var okSrc, okDst bool
	in.Src, okSrc = readPod(src, "source")
	in.Dst, okDst = readPod(dst, "destination")
	if !okSrc || !okDst {
		return in, problems
	}
	in.ByNamespace = make(map[string]netpol.Policies)
	for _, ns := range uniqueStrings(src.Namespace, dst.Namespace) {
		items, err := listTyped[networkingv1.NetworkPolicy](ctx, r, networkPolicyGVR, ns)
		in.ByNamespace[ns] = netpol.Policies{Items: items, Err: err}
		if err != nil {
			problems = append(problems, netpolProblem{code: "policies-unreadable/" + ns,
				text: fmt.Sprintf("NetworkPolicies in %s could not be read: %s", ns, readProblem(err))})
		}
	}
	// Namespace selectors of the destination's ingress rules match the
	// source namespace, those of the source's egress rules the
	// destination namespace.
	in.Namespaces.Labels = make(map[string]map[string]string)
	for _, ns := range uniqueStrings(src.Namespace, dst.Namespace) {
		obj, err := r.GetByGVR(ctx, netpolNamespacesGVR, "", ns)
		if err != nil {
			if in.Namespaces.Err == nil {
				in.Namespaces.Err = errors.New(readProblem(err))
			}
			if !apierrors.IsNotFound(err) {
				problems = append(problems, netpolProblem{code: "namespace-unreadable/" + ns,
					text: fmt.Sprintf("The labels of namespace %s could not be read: %s; rules with a namespaceSelector are unknown", ns, readProblem(err))})
			}
			continue
		}
		in.Namespaces.Labels[ns] = obj.GetLabels()
	}
	return in, problems
}

func uniqueStrings(a, b string) []string {
	if a == b {
		return []string{a}
	}
	return []string{a, b}
}

// netpolPairRows renders the identities, one result per port with its
// egress and ingress sides and the policies behind them, and the note.
func netpolPairRows(deps Deps, depsFor func(RelatedRef) Deps, path []string, src, dst PodRef, in netpol.Input, problems []netpolProblem, now time.Time) []table.Row {
	var rows []table.Row
	text := func(id, label, value string, style bool) {
		st := WhiteStyle()
		if style {
			st = DimStyle()
		}
		r := NewRowItem(id, []string{label, "", "", value}, append(slices.Clone(path), label), st)
		r.details = value
		rows = append(rows, r)
	}
	podText := func(ref PodRef, p netpol.Pod) string {
		s := ref.Namespace + "/" + ref.Name
		uid := p.UID
		if uid == "" {
			uid = string(ref.UID)
		}
		if uid != "" {
			s += " uid " + uid
		}
		if len(p.IPs) > 0 {
			s += ", " + strings.Join(p.IPs, ", ")
		}
		return s
	}
	text("summary/source", "source", podText(src, in.Src), false)
	text("summary/destination", "destination", podText(dst, in.Dst), false)
	text("summary/read", "read", now.UTC().Format(time.RFC3339)+" (a snapshot; open it again to reread)", false)
	fatal := false
	for _, p := range problems {
		sev := diagIncomplete
		rows = append(rows, findingRows(deps, depsFor, path, diagFinding{Code: p.code, Severity: sev, Title: p.text})...)
		fatal = fatal || p.fatal
	}
	if fatal {
		return rows
	}
	var all []networkingv1.NetworkPolicy
	for _, ns := range uniqueStrings(src.Namespace, dst.Namespace) {
		all = append(all, in.ByNamespace[ns].Items...)
	}
	for _, port := range netpol.Ports(in.Dst, all...) {
		in.Port = port
		res := netpol.Evaluate(in)
		rows = append(rows, netpolResultRows(deps, depsFor, path, res)...)
	}
	text("note", "note", netpolNote, true)
	return rows
}

func verdictSeverity(v netpol.Verdict) diagSeverity {
	switch v {
	case netpol.Allowed:
		return diagInfo
	case netpol.Denied:
		return diagError
	default:
		return diagIncomplete
	}
}

func netpolResultRows(deps Deps, depsFor func(RelatedRef) Deps, path []string, res netpol.Result) []table.Row {
	id := "port/" + strings.ReplaceAll(res.Port.String(), " ", "/")
	headText := res.Port.String() + ": " + res.Verdict.String() + " (egress " + res.Egress.Verdict.String() + ", ingress " + res.Ingress.Verdict.String() + ")"
	head := NewRowItem(id, []string{res.Verdict.String(), "", "", headText}, append(slices.Clone(path), res.Port.String()), diagStyle(verdictSeverity(res.Verdict)))
	head.details = headText
	rows := []table.Row{head}
	for _, side := range []netpol.Side{res.Egress, res.Ingress} {
		sid := id + "/" + side.Direction.String()
		where := "egress at the source"
		if side.Direction == netpol.Ingress {
			where = "ingress at the destination"
		}
		sideText := where + ": " + side.Verdict.String() + ". " + side.Reason
		r := NewRowItem(sid, []string{"  " + side.Direction.String(), "", "", sideText}, append(slices.Clone(path), side.Direction.String()), diagStyle(verdictSeverity(side.Verdict)))
		r.details = sideText
		rows = append(rows, r)
		n := 0
		add := func(rr netpol.RuleRef, what string) {
			// One row per rule: a policy can contribute several rules.
			ref := RelatedRef{GVR: networkPolicyGVR, Namespace: rr.Namespace, Name: rr.Policy,
				Relation: what + " " + rr.Field(side.Direction) + ": " + rr.Why}
			rows = append(rows, relatedRowsPrefixed(deps, depsFor, path, fmt.Sprintf("%s/%d/", sid, n), []RelatedRef{ref})...)
			n++
		}
		for _, rr := range side.Allowing {
			add(rr, "allows,")
		}
		for _, rr := range side.Uncertain {
			add(rr, "may allow,")
		}
		if side.Verdict == netpol.Denied {
			for _, rr := range side.Selecting {
				add(rr, "isolates,")
			}
		}
	}
	return rows
}
