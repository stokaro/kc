package nodeaction

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/stokaro/kc/internal/models"
)

const (
	// DefaultRetryDelay is how long a drain waits after an eviction was
	// refused (429, usually a PodDisruptionBudget) before it asks again.
	// kubectl drain waits the same.
	DefaultRetryDelay = 5 * time.Second
	// DefaultPollInterval is how often a drain looks for evicted pods to be
	// gone. kubectl drain polls each pod once a second as well.
	DefaultPollInterval = time.Second
	// DefaultDrainTimeout bounds one node's drain in the options dialog.
	DefaultDrainTimeout = 5 * time.Minute
)

// DrainOptions are the choices of the drain dialog. They have the meaning
// of the kubectl drain flags of the same name.
type DrainOptions struct {
	// GracePeriodSeconds is passed to every eviction. A negative value uses
	// each pod's terminationGracePeriodSeconds.
	GracePeriodSeconds int
	// Timeout bounds the drain of one node, like kubectl's --timeout. Zero
	// waits as long as it takes.
	Timeout time.Duration
	// IgnoreDaemonSets skips DaemonSet pods instead of refusing the drain.
	IgnoreDaemonSets bool
	// DeleteEmptyDirData evicts pods with emptyDir volumes; their data is
	// lost.
	DeleteEmptyDirData bool
	// Force evicts pods without a controller; nothing recreates them.
	Force bool
}

// DefaultDrainOptions are what the dialog starts with. They differ from
// kubectl's defaults in two places: DaemonSet pods are skipped (nearly
// every node runs one, so kubectl's default refuses nearly every drain),
// and the drain of a node gives up after five minutes instead of never.
func DefaultDrainOptions() DrainOptions {
	return DrainOptions{GracePeriodSeconds: -1, Timeout: DefaultDrainTimeout, IgnoreDaemonSets: true}
}

// KubectlArgs returns the kubectl drain arguments for nodes, naming only
// the flags whose value differs from kubectl's default.
func (o DrainOptions) KubectlArgs(nodes []string) []string {
	args := append([]string{"drain"}, nodes...)
	if o.IgnoreDaemonSets {
		args = append(args, "--ignore-daemonsets")
	}
	if o.DeleteEmptyDirData {
		args = append(args, "--delete-emptydir-data")
	}
	if o.Force {
		args = append(args, "--force")
	}
	if o.GracePeriodSeconds >= 0 {
		args = append(args, "--grace-period="+strconv.Itoa(o.GracePeriodSeconds))
	}
	if o.Timeout > 0 {
		args = append(args, "--timeout="+o.Timeout.String())
	}
	return args
}

// Phase is where the drain of a node is.
type Phase string

const (
	PhaseCordoning Phase = "cordoning"
	PhaseEvicting  Phase = "evicting"
	PhaseDrained   Phase = "drained"
	PhaseFailed    Phase = "failed"
	PhaseCanceled  Phase = "canceled"
	PhaseWaiting   Phase = "waiting" // not started yet
)

// NodeProgress is the state of one node's drain.
type NodeProgress struct {
	Node  string
	Phase Phase
	// Evicted counts pods that were evicted and are gone.
	Evicted int
	// Remaining counts pods still to evict or still terminating.
	Remaining int
	// Blocked are pods whose last eviction was refused with 429, usually by
	// a PodDisruptionBudget. The drain asks again after the retry delay.
	Blocked []PodNote
	// Error is why the node failed.
	Error string
}

// Progress is a snapshot of a running drain, one entry per node in order.
type Progress struct {
	Nodes []NodeProgress
}

// DrainAction cordons nodes and evicts their pods.
type DrainAction struct {
	env   Env
	opts  DrainOptions
	nodes []string
	plan  []Selection // per node, what Prepare saw

	// RetryDelay and PollInterval default to DefaultRetryDelay and
	// DefaultPollInterval; tests shorten them.
	RetryDelay   time.Duration
	PollInterval time.Duration
}

// PrepareDrain reads the nodes' pods from the shared cache and applies the
// drain rules. It refuses up front when a node has pods the drain would
// refuse, so a refused drain changes nothing; kubectl cordons the node
// first and only then fails.
func PrepareDrain(ctx context.Context, env Env, nodes []string, opts DrainOptions) (*DrainAction, error) {
	if len(nodes) == 0 {
		return nil, fmt.Errorf("no node selected")
	}
	if env.Pods == nil {
		return nil, fmt.Errorf("drain needs a pod lister")
	}
	a := &DrainAction{env: env, opts: opts, nodes: nodes}
	var refusals []error
	for _, n := range nodes {
		if _, err := nodeUnschedulable(ctx, env, n); err != nil {
			return nil, err
		}
		sel, err := a.selectPods(ctx, n)
		if err != nil {
			return nil, err
		}
		for _, b := range sel.Blocked {
			refusals = append(refusals, fmt.Errorf("node %s: %s", n, b))
		}
		a.plan = append(a.plan, sel)
	}
	if len(refusals) > 0 {
		return nil, &RefusedError{Reasons: refusals}
	}
	return a, nil
}

// RefusedError lists the pods that stop a drain before anything changed.
type RefusedError struct {
	Reasons []error
}

func (e *RefusedError) Error() string {
	msg := "cannot drain: " + e.Reasons[0].Error()
	if len(e.Reasons) > 1 {
		msg += fmt.Sprintf(" (and %d more)", len(e.Reasons)-1)
	}
	return msg
}

func (a *DrainAction) selectPods(ctx context.Context, node string) (Selection, error) {
	pods, err := a.env.Pods(ctx, node)
	if err != nil {
		return Selection{}, fmt.Errorf("listing pods on node %s: %w", node, err)
	}
	return SelectPods(pods, a.opts, func(ns, name string) (bool, error) {
		return daemonSetExists(ctx, a.env, ns, name)
	}), nil
}

// daemonSetExists reads a DaemonSet from the shared cache.
func daemonSetExists(ctx context.Context, env Env, namespace, name string) (bool, error) {
	ctrllog.FromContext(ctx).WithName("nodeaction").V(1).Info("cache get",
		"gvr", "apps/v1/daemonsets", "namespace", namespace, "name", name)
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("apps/v1")
	u.SetKind("DaemonSet")
	err := env.Client.Get(ctx, crclient.ObjectKey{Namespace: namespace, Name: name}, u)
	switch {
	case err == nil:
		return true, nil
	case apierrors.IsNotFound(err):
		return false, nil
	default:
		return false, fmt.Errorf("reading DaemonSet %s/%s: %w", namespace, name, err)
	}
}

// Nodes returns the nodes the drain works on.
func (a *DrainAction) Nodes() []string { return append([]string(nil), a.nodes...) }

// Options returns the options the drain runs with.
func (a *DrainAction) Options() DrainOptions { return a.opts }

// Plan returns what Prepare saw for each node, in node order.
func (a *DrainAction) Plan() []Selection { return append([]Selection(nil), a.plan...) }

// Command is the equivalent kubectl command.
func (a *DrainAction) Command() string {
	return a.env.kubectl(a.opts.KubectlArgs(a.nodes)...)
}

// Confirmation describes the drain for the red dialog.
func (a *DrainAction) Confirmation() models.Confirmation {
	evict, skipped := 0, 0
	var warnings []PodNote
	for _, s := range a.plan {
		evict += len(s.Evict)
		skipped += len(s.Skipped)
		warnings = append(warnings, s.Warnings...)
	}
	title := "Drain node?"
	label := "Node"
	if len(a.nodes) > 1 {
		title = fmt.Sprintf("Drain %d nodes?", len(a.nodes))
		label = "Nodes"
	}
	grace := "each pod's own"
	if a.opts.GracePeriodSeconds >= 0 {
		grace = fmt.Sprintf("%ds", a.opts.GracePeriodSeconds)
	}
	timeout := "none"
	if a.opts.Timeout > 0 {
		timeout = a.opts.Timeout.String() + " per node"
	}
	notes := []string{
		"Cordons the node, then evicts " + plural(evict, "pod") + " through the",
		"Eviction API, which respects PodDisruptionBudgets.",
		"The node stays cordoned afterwards.",
	}
	if skipped > 0 {
		notes = append(notes, "Leaves "+plural(skipped, "DaemonSet or mirror pod")+" alone.")
	}
	const maxWarnings = 3
	for i, w := range warnings {
		if i == maxWarnings {
			notes = append(notes, fmt.Sprintf("... and %d more", len(warnings)-maxWarnings))
			break
		}
		notes = append(notes, "! "+w.Namespace+"/"+w.Name, "  "+w.Message)
	}
	return models.Confirmation{
		Title: title,
		Details: [][2]string{
			{"Context", a.env.Context},
			{label, nodeList(a.nodes)},
			{"Grace period", grace},
			{"Timeout", timeout},
		},
		Notes:  notes,
		Action: "Drain",
	}
}

// Run cordons every node first, as kubectl drain does, and then drains the
// nodes one after another. report receives a snapshot after every change;
// it is called on Run's goroutine. Canceling ctx stops the drain between
// two requests; the nodes stay cordoned and the evictions already accepted
// go on.
func (a *DrainAction) Run(ctx context.Context, report func(Progress)) (Result, error) {
	if report == nil {
		report = func(Progress) {}
	}
	prog := Progress{Nodes: make([]NodeProgress, len(a.nodes))}
	for i, n := range a.nodes {
		prog.Nodes[i] = NodeProgress{Node: n, Phase: PhaseWaiting}
		if i < len(a.plan) {
			prog.Nodes[i].Remaining = len(a.plan[i].Evict)
		}
	}
	snapshot := func() {
		out := Progress{Nodes: make([]NodeProgress, len(prog.Nodes))}
		for i, n := range prog.Nodes {
			n.Blocked = append([]PodNote(nil), n.Blocked...)
			out.Nodes[i] = n
		}
		report(out)
	}
	fail := func(i int, phase Phase, err error) (Result, error) {
		prog.Nodes[i].Phase = phase
		prog.Nodes[i].Error = err.Error()
		snapshot()
		return Result{}, err
	}

	for i, n := range a.nodes {
		prog.Nodes[i].Phase = PhaseCordoning
		snapshot()
		if err := ctx.Err(); err != nil {
			return fail(i, PhaseCanceled, fmt.Errorf("drain canceled: %w", err))
		}
		if err := patchUnschedulable(ctx, a.env, n, true); err != nil {
			return fail(i, PhaseFailed, err)
		}
		prog.Nodes[i].Phase = PhaseWaiting
	}
	snapshot()

	evicted := 0
	for i, n := range a.nodes {
		if err := a.drainNode(ctx, n, &prog.Nodes[i], snapshot); err != nil {
			phase := PhaseFailed
			if errors.Is(err, context.Canceled) {
				phase = PhaseCanceled
			}
			return fail(i, phase, err)
		}
		evicted += prog.Nodes[i].Evicted
	}
	if len(a.nodes) == 1 {
		return Result{Message: fmt.Sprintf("Drained node %s: evicted %s", a.nodes[0], plural(evicted, "pod"))}, nil
	}
	return Result{Message: fmt.Sprintf("Drained %d nodes: evicted %s", len(a.nodes), plural(evicted, "pod"))}, nil
}

// drainNode evicts the pods of one node until none that the rules evict is
// left. It reads the pods again each round, so a pod that the cache had not
// shown yet, or that started before the cordon took effect, is evicted too.
func (a *DrainAction) drainNode(ctx context.Context, node string, np *NodeProgress, snapshot func()) error {
	retry := a.RetryDelay
	if retry <= 0 {
		retry = DefaultRetryDelay
	}
	poll := a.PollInterval
	if poll <= 0 {
		poll = DefaultPollInterval
	}
	if a.opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeoutCause(ctx, a.opts.Timeout, errNodeTimeout)
		defer cancel()
	}
	np.Phase = PhaseEvicting
	requested := make(map[types.UID]bool) // accepted evictions, until the pod is gone
	for {
		if err := ctx.Err(); err != nil {
			return a.stopError(ctx, node, np)
		}
		sel, err := a.selectPods(ctx, node)
		if err != nil {
			if ctx.Err() != nil {
				return a.stopError(ctx, node, np)
			}
			return err
		}
		if len(sel.Blocked) > 0 {
			// A lookup the cancel interrupted (a DaemonSet read) shows up as
			// a blocked pod; report the cancel, not the lookup error.
			if ctx.Err() != nil {
				return a.stopError(ctx, node, np)
			}
			return fmt.Errorf("drain node %s: %s", node, sel.Blocked[0])
		}
		countGone(np, requested, sel.Evict)
		np.Remaining = len(sel.Evict)
		if len(sel.Evict) == 0 {
			np.Blocked = nil
			np.Phase = PhaseDrained
			snapshot()
			return nil
		}

		if err := a.evictRound(ctx, node, np, sel.Evict, requested); err != nil {
			return err
		}
		snapshot()

		wait := poll
		if len(np.Blocked) > 0 {
			wait = retry
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return a.stopError(ctx, node, np)
		case <-t.C:
		}
	}
}

// errNodeTimeout is the cause of a node context that ran out of the
// drain's per-node timeout.
var errNodeTimeout = errors.New("drain timeout")

// stopError explains why a node's drain ended early: its timeout passed,
// the user canceled it, or the caller's context ended.
func (a *DrainAction) stopError(ctx context.Context, node string, np *NodeProgress) error {
	left := plural(np.Remaining, "pod")
	switch cause := context.Cause(ctx); {
	case errors.Is(cause, errNodeTimeout):
		return fmt.Errorf("drain node %s: timed out after %s with %s left", node, a.opts.Timeout, left)
	case errors.Is(ctx.Err(), context.Canceled):
		return fmt.Errorf("drain node %s canceled with %s left: %w", node, left, context.Canceled)
	default:
		return fmt.Errorf("drain node %s stopped with %s left: %w", node, left, cause)
	}
}

// evict asks the API server to evict pod, which checks its
// PodDisruptionBudgets.
// countGone counts the pods with an accepted eviction that are no longer
// among pods as evicted, and forgets them.
func countGone(np *NodeProgress, requested map[types.UID]bool, pods []corev1.Pod) {
	present := make(map[types.UID]bool, len(pods))
	for _, p := range pods {
		present[p.UID] = true
	}
	for uid := range requested {
		if !present[uid] {
			np.Evicted++
			delete(requested, uid)
		}
	}
}

// evictRound asks to evict every pod without an accepted eviction. Pods the
// API refuses to evict for now go to np.Blocked for the next round.
func (a *DrainAction) evictRound(ctx context.Context, node string, np *NodeProgress, pods []corev1.Pod, requested map[types.UID]bool) error {
	np.Blocked = nil
	for i := range pods {
		pod := &pods[i]
		if requested[pod.UID] {
			continue
		}
		if err := ctx.Err(); err != nil {
			return a.stopError(ctx, node, np)
		}
		err := a.evict(ctx, pod)
		switch {
		case err == nil:
			requested[pod.UID] = true
		case apierrors.IsNotFound(err):
			// Gone already; the next round counts it.
			requested[pod.UID] = true
		case apierrors.IsTooManyRequests(err):
			np.Blocked = append(np.Blocked, PodNote{Namespace: pod.Namespace, Name: pod.Name, Message: evictionRefusal(err)})
		case apierrors.IsForbidden(err) && apierrors.HasStatusCause(err, corev1.NamespaceTerminatingCause):
			// The namespace is being deleted; the pod goes with it.
			// kubectl retries as well.
			np.Blocked = append(np.Blocked, PodNote{Namespace: pod.Namespace, Name: pod.Name, Message: "namespace is terminating"})
		default:
			if ctx.Err() != nil {
				return a.stopError(ctx, node, np)
			}
			return fmt.Errorf("evicting pod %s/%s: %w", pod.Namespace, pod.Name, err)
		}
	}
	return nil
}

func (a *DrainAction) evict(ctx context.Context, pod *corev1.Pod) error {
	eviction := &policyv1.Eviction{
		Namespace: pod.Namespace, Name: pod.Name,
	}
	if a.opts.GracePeriodSeconds >= 0 {
		grace := int64(a.opts.GracePeriodSeconds)
		eviction.DeleteOptions = &metav1.DeleteOptions{GracePeriodSeconds: &grace}
	}
	target := &corev1.Pod{Namespace: pod.Namespace, Name: pod.Name}
	ctrllog.FromContext(ctx).WithName("nodeaction").Info("client create",
		"gvr", "v1/pods/eviction", "namespace", pod.Namespace, "name", pod.Name, "node", pod.Spec.NodeName)
	return a.env.Client.SubResource("eviction").Create(ctx, target, eviction)
}

// evictionRefusal shortens a 429 to the server's reason, which names the
// disruption budget.
func evictionRefusal(err error) string {
	var status apierrors.APIStatus
	if errors.As(err, &status) {
		if details := status.Status().Details; details != nil {
			for _, c := range details.Causes {
				if c.Type == policyv1.DisruptionBudgetCause && c.Message != "" {
					return c.Message
				}
			}
		}
		if msg := status.Status().Message; msg != "" {
			return msg
		}
	}
	return err.Error()
}
