package models

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/util/jsonpath"

	"github.com/stokaro/kc/pkg/appconfig"
)

// healthAdapter is the compiled health config of one resource type (see
// appconfig.HealthRule). Row colors, the faults filter and /diagnosis all
// read its verdict, so they never disagree about one object.
type healthAdapter struct {
	key   string // the config key, for messages
	rules []*healthRule
}

type healthRule struct {
	condition string // condition type, or "" for a jsonPath rule
	status    string // canonical condition status
	expr      string // the jsonPath as configured
	value     string
	health    RowHealth

	mu   sync.Mutex         // jsonpath.JSONPath is not safe for concurrent use
	path *jsonpath.JSONPath // nil for a condition rule
}

// describe names what the rule matches, as the config spells it.
func (r *healthRule) describe() string {
	if r.path == nil {
		return "status.conditions[" + r.condition + "]=" + r.status
	}
	return r.expr + "=" + r.value
}

// healthAdapters holds the installed adapters by GVR. Rows are styled on
// several goroutines, so it is swapped whole, never changed in place.
var healthAdapters atomic.Pointer[map[schema.GroupVersionResource]*healthAdapter]

// healthAdapterFor returns the adapter of gvr, or nil. A type with a
// built-in rule never has one.
func healthAdapterFor(gvr schema.GroupVersionResource) *healthAdapter {
	m := healthAdapters.Load()
	if m == nil {
		return nil
	}
	return (*m)[gvr]
}

// InstallHealthAdapters compiles the health config and makes it the one
// rows and /diagnosis use. An entry with any problem is left out whole, so
// a half-read entry never decides a health; its problems are returned,
// sorted by key, for the caller to report. Entries for types with a
// built-in rule are left out too: the built-in rule wins.
func InstallHealthAdapters(cfg map[string][]appconfig.HealthRule) []error {
	errs := appconfig.ValidateHealth(cfg)
	bad := make(map[string]bool, len(errs))
	for k, rules := range cfg {
		if len(appconfig.ValidateHealthEntry(k, rules)) > 0 {
			bad[k] = true
		}
	}
	adapters := make(map[schema.GroupVersionResource]*healthAdapter, len(cfg))
	for _, k := range sortedKeys(cfg) {
		if bad[k] {
			continue
		}
		group, version, resource, _ := appconfig.ParseColumnsKey(k)
		gvr := schema.GroupVersionResource{Group: group, Version: version, Resource: resource}
		if _, builtin := healthRules[gvr]; builtin {
			errs = append(errs, fmt.Errorf("health %q: kc has a built-in rule for this type; the entry is ignored", k))
			continue
		}
		a := &healthAdapter{key: k}
		for _, r := range cfg[k] {
			a.rules = append(a.rules, compileHealthRule(r))
		}
		adapters[gvr] = a
	}
	healthAdapters.Store(&adapters)
	return errs
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// compileHealthRule compiles a rule that appconfig.HealthRule.Validate
// accepted.
func compileHealthRule(r appconfig.HealthRule) *healthRule {
	out := &healthRule{health: rowHealthOf(r.Health)}
	if c := strings.TrimSpace(r.Condition); c != "" {
		out.condition, out.status = c, r.ConditionStatus()
		return out
	}
	out.expr, out.value = strings.TrimSpace(r.JSONPath), strings.TrimSpace(string(r.Value))
	out.path, _ = appconfig.ParseCustomColumn(out.expr) // validated
	return out
}

func rowHealthOf(s string) RowHealth {
	switch s {
	case appconfig.HealthOK:
		return RowHealthOK
	case appconfig.HealthSucceeded:
		return RowHealthSucceeded
	case appconfig.HealthNotReady:
		return RowHealthNotReady
	case appconfig.HealthFailed:
		return RowHealthFailed
	}
	return RowHealthUnknown
}

// healthVerdict is what an adapter concluded about one object.
type healthVerdict struct {
	// Health is the state rows show.
	Health RowHealth
	// RuleHealth is the state the matched rule sets, before the
	// generation check.
	RuleHealth RowHealth
	// Rule is the 1-based index of the rule that matched, 0 for none.
	Rule int
	// Matched names the field the rule matched, such as
	// "status.conditions[Ready]=True".
	Matched string
	// Reason and Message come from the matched condition.
	Reason, Message string
	// Since is the matched condition's lastTransitionTime.
	Since time.Time
	// Stale is set when the status was written for an older generation;
	// Health is then notReady unless the rules said failed.
	Stale      bool
	Generation int64
	Observed   int64
	// Err is a field of the wrong type; Health is then unknown.
	Err error
}

// condition is one entry of status.conditions, read leniently.
type condition struct {
	Type, Status, Reason, Message string
	LastTransition                time.Time
	ObservedGeneration            int64 // 0 when absent
}

// evaluate applies the rules to u. A field of the wrong type makes the
// verdict unknown with Err set; a missing field just matches nothing.
func (a *healthAdapter) evaluate(u *unstructured.Unstructured) healthVerdict {
	var v healthVerdict
	if u == nil {
		return v
	}
	conds, err := readConditions(u)
	if err != nil {
		v.Err = err
		return v
	}
	var matched *condition
	for i, r := range a.rules {
		c, ok, err := r.match(u, conds)
		if err != nil {
			v.Err = err
			return v
		}
		if !ok {
			continue
		}
		v.Health, v.RuleHealth, v.Rule, v.Matched = r.health, r.health, i+1, r.describe()
		if c != nil {
			matched = c
			v.Reason, v.Message, v.Since = c.Reason, c.Message, c.LastTransition
		}
		break
	}
	if v.Rule == 0 {
		return v
	}
	if err := v.checkGeneration(u, matched); err != nil {
		return healthVerdict{Err: err}
	}
	return v
}

// checkGeneration marks a verdict whose status is older than the spec.
// The status counts as written for generation N when status.observedGeneration
// is N, or when the matched condition says so; a field that is absent is
// not checked.
func (v *healthVerdict) checkGeneration(u *unstructured.Unstructured, matched *condition) error {
	gen := u.GetGeneration()
	if gen == 0 {
		return nil
	}
	observed, found, err := unstructured.NestedFieldNoCopy(u.Object, "status", "observedGeneration")
	if err != nil {
		return fmt.Errorf("status.observedGeneration: %w", err)
	}
	if found {
		n, ok := observed.(int64)
		if !ok {
			return fmt.Errorf("status.observedGeneration is %T, want an integer", observed)
		}
		if n < gen {
			v.Stale, v.Observed = true, n
		}
	}
	if matched != nil && matched.ObservedGeneration > 0 && matched.ObservedGeneration < gen {
		if !v.Stale || matched.ObservedGeneration < v.Observed {
			v.Stale, v.Observed = true, matched.ObservedGeneration
		}
	}
	if v.Stale {
		v.Generation = gen
		if v.Health != RowHealthFailed {
			v.Health = RowHealthNotReady
		}
	}
	return nil
}

// match reports whether the rule matches u. For a condition rule it also
// returns the condition.
func (r *healthRule) match(u *unstructured.Unstructured, conds []condition) (*condition, bool, error) {
	if r.path == nil {
		for i := range conds {
			if conds[i].Type == r.condition && strings.EqualFold(conds[i].Status, r.status) {
				return &conds[i], true, nil
			}
		}
		return nil, false, nil
	}
	r.mu.Lock()
	results, err := r.path.FindResults(u.Object)
	r.mu.Unlock()
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", r.expr, err)
	}
	for _, rs := range results {
		for _, rv := range rs {
			text, err := scalarText(rv)
			if err != nil {
				return nil, false, fmt.Errorf("%s: %w", r.expr, err)
			}
			if text == r.value {
				return nil, true, nil
			}
		}
	}
	return nil, false, nil
}

// scalarText prints a JSONPath result that is a string, number or
// boolean; an object or a list is a field of the wrong type.
func scalarText(rv reflect.Value) (string, error) {
	for rv.Kind() == reflect.Interface || rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return "", nil
		}
		rv = rv.Elem()
	}
	switch rv.Kind() {
	case reflect.String:
		return rv.String(), nil
	case reflect.Bool, reflect.Int, reflect.Int32, reflect.Int64, reflect.Float32, reflect.Float64:
		return fmt.Sprint(rv.Interface()), nil
	}
	return "", fmt.Errorf("is a %s, want a string, number or boolean", rv.Kind())
}

// readConditions reads status.conditions. An absent list is no
// conditions; a list of the wrong shape is an error. Entries without a
// string type are skipped.
func readConditions(u *unstructured.Unstructured) ([]condition, error) {
	raw, found, err := unstructured.NestedFieldNoCopy(u.Object, "status", "conditions")
	if err != nil {
		return nil, fmt.Errorf("status.conditions: %w", err)
	}
	if !found || raw == nil {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("status.conditions is %T, want a list", raw)
	}
	out := make([]condition, 0, len(list))
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		c := condition{
			Type:    stringField(m, "type"),
			Status:  stringField(m, "status"),
			Reason:  stringField(m, "reason"),
			Message: stringField(m, "message"),
		}
		if c.Type == "" {
			continue
		}
		var t metav1.Time
		if err := t.UnmarshalQueryParameter(stringField(m, "lastTransitionTime")); err == nil {
			c.LastTransition = t.Time
		}
		if n, ok := m["observedGeneration"].(int64); ok {
			c.ObservedGeneration = n
		}
		out = append(out, c)
	}
	return out, nil
}

func stringField(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

// summary is the verdict as one line for /diagnosis.
func (v healthVerdict) summary(key string) string {
	if v.Err != nil {
		return fmt.Sprintf("health: unknown, the %s health config cannot read this object: %v", key, v.Err)
	}
	if v.Rule == 0 {
		return fmt.Sprintf("health: unknown, no rule of the %s health config matches", key)
	}
	s := fmt.Sprintf("health: %s, by rule %d of %s (%s", healthName(v.Health), v.Rule, key, v.Matched)
	if v.Reason != "" {
		s += ", reason " + v.Reason
	}
	s += ")"
	if v.Stale {
		s += fmt.Sprintf("; the status is for generation %d, the spec is at %d", v.Observed, v.Generation)
	}
	return s
}

func healthName(h RowHealth) string {
	switch h {
	case RowHealthOK:
		return appconfig.HealthOK
	case RowHealthSucceeded:
		return appconfig.HealthSucceeded
	case RowHealthNotReady:
		return appconfig.HealthNotReady
	case RowHealthFailed:
		return appconfig.HealthFailed
	}
	return "unknown"
}
