package appconfig

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Health states a health rule can set. They are the row states of kc:
// ok and succeeded keep the row's color, notReady and failed color it and
// count as faults.
const (
	HealthOK        = "ok"
	HealthSucceeded = "succeeded"
	HealthNotReady  = "notReady"
	HealthFailed    = "failed"
)

// HealthRule is one rule of the health config of a resource type. The
// rules of a type are tried in order and the first that matches sets the
// health; when none matches the health is unknown.
//
// A rule matches either a status condition (Condition and Status) or a
// field (JSONPath and Value), never both.
type HealthRule struct {
	// Condition is the type of an entry of status.conditions, such as
	// "Ready". Kubernetes condition types are case-sensitive.
	Condition string `json:"condition,omitempty"`
	// Status is the condition status the rule wants: True (the default),
	// False or Unknown, in any case.
	Status Scalar `json:"status,omitempty"`
	// JSONPath is a kubectl custom-columns expression, such as
	// ".status.health.status". The rule matches when one of its values,
	// printed as text, equals Value.
	JSONPath string `json:"jsonPath,omitempty"`
	// Value is the text the JSONPath value must equal.
	Value Scalar `json:"value,omitempty"`
	// Health is the state the rule sets: ok, succeeded, notReady or failed.
	Health string `json:"health"`
}

// Scalar is a config value written as a string. YAML reads True, 3 or
// yes-like words as other types; Scalar takes them as the text they were
// written as, so `status: True` works without quotes.
type Scalar string

// UnmarshalJSON accepts a string, a boolean or a number.
func (s *Scalar) UnmarshalJSON(b []byte) error {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	switch t := v.(type) {
	case string:
		*s = Scalar(t)
	case bool:
		// YAML turns True into true; Kubernetes spells it True.
		*s = "False"
		if t {
			*s = "True"
		}
	case float64:
		*s = Scalar(strconv.FormatFloat(t, 'f', -1, 64))
	case nil:
		*s = ""
	default:
		return fmt.Errorf("want a string, got %s", string(b))
	}
	return nil
}

// ConditionStatus returns the condition status the rule wants, spelled
// as Kubernetes does, or "" for a value that is not a condition status.
func (r HealthRule) ConditionStatus() string {
	s := strings.TrimSpace(string(r.Status))
	if s == "" {
		return "True"
	}
	for _, want := range []string{"True", "False", "Unknown"} {
		if strings.EqualFold(s, want) {
			return want
		}
	}
	return ""
}

// Validate checks one rule.
func (r HealthRule) Validate() error {
	cond, path := strings.TrimSpace(r.Condition), strings.TrimSpace(r.JSONPath)
	switch r.Health {
	case HealthOK, HealthSucceeded, HealthNotReady, HealthFailed:
	case "":
		return fmt.Errorf("health is empty; want ok, succeeded, notReady or failed")
	default:
		return fmt.Errorf("health %q: want ok, succeeded, notReady or failed", r.Health)
	}
	switch {
	case cond != "" && path != "":
		return fmt.Errorf("set either condition or jsonPath, not both")
	case cond == "" && path == "":
		return fmt.Errorf("set condition or jsonPath")
	case cond != "":
		if r.Value != "" {
			return fmt.Errorf("condition %s: value applies to jsonPath rules; use status", cond)
		}
		if r.ConditionStatus() == "" {
			return fmt.Errorf("condition %s: status %q: want True, False or Unknown", cond, r.Status)
		}
		return nil
	}
	if r.Status != "" {
		return fmt.Errorf("jsonPath %s: status applies to condition rules; use value", path)
	}
	if strings.TrimSpace(string(r.Value)) == "" {
		return fmt.Errorf("jsonPath %s: value is empty", path)
	}
	if _, err := ParseCustomColumn(path); err != nil {
		return fmt.Errorf("jsonPath %s: %w", path, err)
	}
	return nil
}

// matchKey identifies what a rule matches, so a later rule that matches
// the same thing is found: it can never apply.
func (r HealthRule) matchKey() string {
	if c := strings.TrimSpace(r.Condition); c != "" {
		return "condition " + c + "=" + r.ConditionStatus()
	}
	path := strings.TrimSpace(r.JSONPath)
	if tmpl, err := CustomColumnTemplate(path); err == nil {
		path = tmpl // ".a" and "{.a}" are the same path
	}
	return "jsonPath " + path + "=" + strings.TrimSpace(string(r.Value))
}

// ValidateHealth checks the health config and returns one error per bad
// key or rule, sorted by key. A rule that matches what an earlier rule of
// the same type matches is an error: the first match wins, so it would
// never apply.
func ValidateHealth(health map[string][]HealthRule) []error {
	keys := make([]string, 0, len(health))
	for k := range health {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var errs []error
	for _, k := range keys {
		if err := ValidateHealthEntry(k, health[k]); err != nil {
			errs = append(errs, err...)
		}
	}
	return errs
}

// ValidateHealthEntry checks the rules of one key.
func ValidateHealthEntry(key string, rules []HealthRule) []error {
	if _, _, _, err := ParseColumnsKey(key); err != nil {
		return []error{fmt.Errorf("health key %q: want group/version/resource, e.g. example.com/v1/widgets", key)}
	}
	if len(rules) == 0 {
		return []error{fmt.Errorf("health %q: no rules", key)}
	}
	var errs []error
	seen := make(map[string]int, len(rules))
	for i, r := range rules {
		if err := r.Validate(); err != nil {
			errs = append(errs, fmt.Errorf("health %q rule %d: %w", key, i+1, err))
			continue
		}
		m := r.matchKey()
		if first, dup := seen[m]; dup {
			errs = append(errs, fmt.Errorf("health %q rule %d: matches the same %s as rule %d and never applies", key, i+1, m, first))
			continue
		}
		seen[m] = i + 1
	}
	return errs
}
