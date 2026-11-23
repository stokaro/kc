package nodeaction

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// BudgetLister lists the PodDisruptionBudgets of a namespace. kc backs it
// with the same scope decision as a folder list (shared cache, namespace
// cache, or a Forbidden error without a request), so a user who may not
// read budgets gets an error, not an empty list.
type BudgetLister func(ctx context.Context, namespace string) ([]policyv1.PodDisruptionBudget, error)

// BudgetImpact is one PodDisruptionBudget that covers pods the drain plans
// to evict, as its status read when the drain was prepared. It is an
// observation: the disruption controller updates the status, and the API
// server decides each eviction when it arrives.
type BudgetImpact struct {
	Namespace, Name string
	// Selector is the budget's selector as a label selector string.
	Selector string
	// The status fields as the API reported them.
	CurrentHealthy, DesiredHealthy, ExpectedPods, DisruptionsAllowed int32
	// UnhealthyPolicy is spec.unhealthyPodEvictionPolicy, IfHealthyBudget
	// when unset.
	UnhealthyPolicy policyv1.UnhealthyPodEvictionPolicyType
	// Stale is set when status.observedGeneration is behind the budget's
	// generation. The Eviction API refuses the budget's pods until the
	// controller catches up.
	Stale bool
	// Pods are the planned pods the budget covers; NotReady counts those
	// whose Ready condition is not True.
	Pods     []string
	NotReady int
}

// unhealthyFree reports whether the Eviction API evicts a covered pod that
// is not ready without using the budget's allowance: always with
// AlwaysAllow, and with IfHealthyBudget while the budget is met.
func (b BudgetImpact) unhealthyFree() bool {
	if b.UnhealthyPolicy == policyv1.AlwaysAllow {
		return true
	}
	return b.DesiredHealthy > 0 && b.CurrentHealthy >= b.DesiredHealthy
}

// Waiting estimates how many of the covered pods the Eviction API refuses
// at first: those that need the allowance beyond disruptionsAllowed, or
// all of them while the status is stale. They are evicted once
// replacements are healthy and the controller raises the allowance; the
// estimate does not say whether that happens.
func (b BudgetImpact) Waiting() int {
	need := len(b.Pods)
	if b.unhealthyFree() {
		need -= b.NotReady
	}
	if b.Stale {
		return need
	}
	return max(0, need-int(b.DisruptionsAllowed))
}

// String is "shop/web-pdb allows 1 now (healthy 3, desired 2, expected 3),
// covers 2 planned pods".
func (b BudgetImpact) String() string {
	s := fmt.Sprintf("%s/%s allows %d now (healthy %d, desired %d, expected %d), covers %s",
		b.Namespace, b.Name, b.DisruptionsAllowed, b.CurrentHealthy, b.DesiredHealthy, b.ExpectedPods, plural(len(b.Pods), "planned pod"))
	if b.NotReady > 0 {
		s += fmt.Sprintf(", %d not ready (%s)", b.NotReady, b.UnhealthyPolicy)
	}
	if b.Stale {
		s += "; status is stale, evictions are refused until it catches up"
	}
	return s
}

// WorkloadImpact counts the planned pods of one controller.
type WorkloadImpact struct {
	Namespace, Kind, Name string
	Pods                  int
}

func (w WorkloadImpact) String() string {
	if w.Kind == "" {
		return fmt.Sprintf("%s without a controller in %s", plural(w.Pods, "pod"), w.Namespace)
	}
	return fmt.Sprintf("%s %s/%s: %s", w.Kind, w.Namespace, w.Name, plural(w.Pods, "pod"))
}

// Impact is what the planned evictions touch, as read when the drain was
// prepared.
type Impact struct {
	// Workloads group the planned pods by controller, most pods first.
	Workloads []WorkloadImpact
	// Read is false when the drain had no budget lister; the budget fields
	// are then empty, not "no budgets".
	Read bool
	// Budgets cover planned pods, those with the most waiting evictions
	// first.
	Budgets []BudgetImpact
	// Uncovered counts the planned pods that no readable budget covers.
	Uncovered int
	// Bypass counts pending, finished or terminating pods, which the
	// Eviction API evicts without checking budgets.
	Bypass int
	// MultiBudget are planned pods more than one budget covers. The
	// Eviction API refuses to evict such a pod with an error.
	MultiBudget []PodNote
	// Unreadable are namespaces whose budgets could not be read: their
	// pods' budgets are unknown, not absent.
	Unreadable []PodNote
}

// bypassesBudgets reports whether the Eviction API evicts p without
// checking budgets: a pending, finished or terminating pod.
func bypassesBudgets(p *corev1.Pod) bool {
	switch p.Status.Phase {
	case corev1.PodPending, corev1.PodSucceeded, corev1.PodFailed:
		return true
	}
	return p.DeletionTimestamp != nil
}

func podReady(p *corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// budgetsCovering returns the budgets of pdbs that cover pod as the
// Eviction API matches them: a budget whose selector is invalid, missing
// or empty covers nothing.
func budgetsCovering(pdbs []policyv1.PodDisruptionBudget, pod *corev1.Pod) []*policyv1.PodDisruptionBudget {
	var out []*policyv1.PodDisruptionBudget
	for i := range pdbs {
		pdb := &pdbs[i]
		if pdb.Namespace != pod.Namespace || pdb.Spec.Selector == nil {
			continue
		}
		sel, err := metav1.LabelSelectorAsSelector(pdb.Spec.Selector)
		if err != nil || sel.Empty() || !sel.Matches(labels.Set(pod.Labels)) {
			continue
		}
		out = append(out, pdb)
	}
	return out
}

// impactBuilder collects the impact over the pods of every node.
type impactBuilder struct {
	list      BudgetLister
	im        Impact
	workloads map[string]*WorkloadImpact
	budgets   map[string]*BudgetImpact
	nsBudgets map[string][]policyv1.PodDisruptionBudget
	nsErr     map[string]error
}

func newImpactBuilder(list BudgetLister) *impactBuilder {
	return &impactBuilder{
		list: list, im: Impact{Read: list != nil},
		workloads: make(map[string]*WorkloadImpact),
		budgets:   make(map[string]*BudgetImpact),
		nsBudgets: make(map[string][]policyv1.PodDisruptionBudget),
		nsErr:     make(map[string]error),
	}
}

// budgetsOf lists a namespace's budgets once.
func (b *impactBuilder) budgetsOf(ctx context.Context, ns string) ([]policyv1.PodDisruptionBudget, error) {
	if err, failed := b.nsErr[ns]; failed {
		return nil, err
	}
	if pdbs, ok := b.nsBudgets[ns]; ok {
		return pdbs, nil
	}
	pdbs, err := b.list(ctx, ns)
	if err != nil {
		b.nsErr[ns] = err
		return nil, err
	}
	b.nsBudgets[ns] = pdbs
	return pdbs, nil
}

func (b *impactBuilder) addWorkload(pod *corev1.Pod) {
	w := WorkloadImpact{Namespace: pod.Namespace}
	if ref := metav1.GetControllerOf(pod); ref != nil {
		w.Kind, w.Name = ref.Kind, ref.Name
	}
	key := w.Namespace + "/" + w.Kind + "/" + w.Name
	if b.workloads[key] == nil {
		b.workloads[key] = &w
	}
	b.workloads[key].Pods++
}

func (b *impactBuilder) add(ctx context.Context, pods []corev1.Pod) {
	for i := range pods {
		pod := &pods[i]
		b.addWorkload(pod)
		if b.list == nil {
			continue
		}
		if bypassesBudgets(pod) {
			b.im.Bypass++
			continue
		}
		pdbs, err := b.budgetsOf(ctx, pod.Namespace)
		if err != nil {
			continue
		}
		covering := budgetsCovering(pdbs, pod)
		names := make([]string, 0, len(covering))
		for _, pdb := range covering {
			bi := b.budget(pdb)
			bi.Pods = append(bi.Pods, pod.Name)
			if !podReady(pod) {
				bi.NotReady++
			}
			names = append(names, pdb.Name)
		}
		switch len(covering) {
		case 0:
			b.im.Uncovered++
		case 1:
		default:
			b.im.MultiBudget = append(b.im.MultiBudget, PodNote{Namespace: pod.Namespace, Name: pod.Name,
				Message: "covered by " + strings.Join(names, ", ") + "; the Eviction API refuses a pod with more than one budget, and the drain stops there"})
		}
	}
}

// budget returns the impact entry of pdb, adding it when new.
func (b *impactBuilder) budget(pdb *policyv1.PodDisruptionBudget) *BudgetImpact {
	key := pdb.Namespace + "/" + pdb.Name
	if bi, ok := b.budgets[key]; ok {
		return bi
	}
	policy := policyv1.IfHealthyBudget
	if pdb.Spec.UnhealthyPodEvictionPolicy != nil {
		policy = *pdb.Spec.UnhealthyPodEvictionPolicy
	}
	sel := ""
	if s, err := metav1.LabelSelectorAsSelector(pdb.Spec.Selector); err == nil {
		sel = s.String()
	}
	bi := &BudgetImpact{
		Namespace: pdb.Namespace, Name: pdb.Name, Selector: sel,
		CurrentHealthy: pdb.Status.CurrentHealthy, DesiredHealthy: pdb.Status.DesiredHealthy,
		ExpectedPods: pdb.Status.ExpectedPods, DisruptionsAllowed: pdb.Status.DisruptionsAllowed,
		UnhealthyPolicy: policy,
		Stale:           pdb.Status.ObservedGeneration < pdb.Generation,
	}
	b.budgets[key] = bi
	return bi
}

func (b *impactBuilder) result() Impact {
	im := b.im
	for _, w := range b.workloads {
		im.Workloads = append(im.Workloads, *w)
	}
	slices.SortFunc(im.Workloads, func(x, y WorkloadImpact) int {
		if d := y.Pods - x.Pods; d != 0 {
			return d
		}
		return strings.Compare(x.Namespace+"/"+x.Kind+"/"+x.Name, y.Namespace+"/"+y.Kind+"/"+y.Name)
	})
	for _, bi := range b.budgets {
		im.Budgets = append(im.Budgets, *bi)
	}
	slices.SortFunc(im.Budgets, func(x, y BudgetImpact) int {
		if d := y.Waiting() - x.Waiting(); d != 0 {
			return d
		}
		return strings.Compare(x.Namespace+"/"+x.Name, y.Namespace+"/"+y.Name)
	})
	for _, ns := range slices.Sorted(maps.Keys(b.nsErr)) {
		im.Unreadable = append(im.Unreadable, PodNote{Namespace: ns, Message: "PodDisruptionBudgets could not be read: " + b.nsErr[ns].Error()})
	}
	return im
}

// budgetOf returns the planned budget that covers the pod, when exactly
// one does.
func (im Impact) budgetOf(namespace, pod string) (BudgetImpact, bool) {
	var found []BudgetImpact
	for _, b := range im.Budgets {
		if b.Namespace == namespace && slices.Contains(b.Pods, pod) {
			found = append(found, b)
		}
	}
	if len(found) != 1 {
		return BudgetImpact{}, false
	}
	return found[0], true
}
