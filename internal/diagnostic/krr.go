package diagnostic

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
)

// krrResult is the part of `krr simple -f json` (robusta_krr Result) kc
// reads. KRR has no output version, so the parser checks the shape: the
// fields below must be there with these types.
type krrResult struct {
	Scans       *[]krrScan       `json:"scans"`
	Strategy    *krrStrategy     `json:"strategy"`
	Description *string          `json:"description"`
	Errors      []map[string]any `json:"errors"`
}

type krrStrategy struct {
	Name     *string        `json:"name"`
	Settings map[string]any `json:"settings"`
}

type krrScan struct {
	Object      *krrObject     `json:"object"`
	Recommended *krrRecommends `json:"recommended"`
	Severity    string         `json:"severity"`
}

type krrObject struct {
	Name        string         `json:"name"`
	Container   string         `json:"container"`
	Namespace   string         `json:"namespace"`
	Kind        string         `json:"kind"`
	Pods        []krrPod       `json:"pods"`
	HPA         map[string]any `json:"hpa"`
	Allocations *krrAllocation `json:"allocations"`
}

type krrPod struct {
	Name    string `json:"name"`
	Deleted bool   `json:"deleted"`
}

type krrAllocation struct {
	Requests map[string]json.RawMessage `json:"requests"`
	Limits   map[string]json.RawMessage `json:"limits"`
	Info     map[string]*string         `json:"info"`
}

type krrRecommends = krrAllocation

// krrResources are the resources KRR recommends, in column order.
var krrResources = []string{"cpu", "memory"}

// ParseKRR reads the JSON output of `krr simple -f json` and keeps the
// scans inside scopes. Values are shown as "current -> recommended";
// kc never applies them.
func ParseKRR(stdout []byte, scopes []Scope) (Report, error) {
	var res krrResult
	dec := json.NewDecoder(bytes.NewReader(stdout))
	if err := dec.Decode(&res); err != nil {
		return Report{}, fmt.Errorf("krr output is not JSON: %w (run krr with -f json)", err)
	}
	if dec.More() {
		return Report{}, errors.New("krr output has more than one JSON document")
	}
	if res.Scans == nil || res.Strategy == nil || res.Strategy.Name == nil {
		return Report{}, errors.New("krr output has no scans or strategy; this krr version or formatter is not supported (kc reads `krr simple -f json`)")
	}
	rep := Report{
		Tool:    "krr",
		Columns: []string{"NAMESPACE", "KIND", "NAME", "CONTAINER", "PODS", "CPU REQUEST", "CPU LIMIT", "MEMORY REQUEST", "MEMORY LIMIT", "SEVERITY", "INFO"},
	}
	rep.Summary = append(rep.Summary, krrStrategyLines(*res.Strategy)...)
	if len(scopes) > 0 {
		names := make([]string, len(scopes))
		for i, s := range scopes {
			names[i] = s.String()
		}
		rep.Summary = append(rep.Summary, "Scope: "+strings.Join(names, ", "))
	}
	outside := 0
	for i, scan := range *res.Scans {
		if scan.Object == nil || scan.Object.Name == "" || scan.Object.Kind == "" || scan.Object.Allocations == nil || scan.Recommended == nil {
			return Report{}, fmt.Errorf("krr scan %d has no object, kind, name, allocations or recommendation", i)
		}
		o := scan.Object
		if !inScopes(scopes, o.Namespace, o.Kind, o.Name) {
			outside++
			continue
		}
		row, err := krrRow(scan)
		if err != nil {
			return Report{}, fmt.Errorf("krr scan %d (%s %s/%s): %w", i, o.Kind, o.Namespace, o.Name, err)
		}
		rep.Rows = append(rep.Rows, row)
	}
	slices.SortStableFunc(rep.Rows, func(a, b []string) int {
		return strings.Compare(strings.Join(a[:4], "\x00"), strings.Join(b[:4], "\x00"))
	})
	rep.Summary = append(rep.Summary, fmt.Sprintf("Scans: %d shown, %d outside the scope.", len(rep.Rows), outside))
	for _, e := range res.Errors {
		rep.ToolErrors = append(rep.ToolErrors, krrErrorText(e))
	}
	rep.Notes = []string{
		"Values are current -> recommended. kc shows them and never changes requests or limits.",
		"KRR computes them from usage history in Prometheus; a workload younger than the history window, or with too few samples, gets ? and an INFO reason.",
		"unset means no request or limit is set (current) or KRR recommends none (recommended).",
	}
	if res.Description != nil && *res.Description != "" {
		rep.Notes = append(rep.Notes, "Strategy: "+strings.Join(strings.Fields(*res.Description), " "))
	}
	return rep, nil
}

// krrStrategyLines describe the observation window and the strategy
// settings.
func krrStrategyLines(s krrStrategy) []string {
	lines := []string{"Strategy: " + *s.Name}
	if h, ok := s.Settings["history_duration"].(float64); ok {
		lines = append(lines, fmt.Sprintf("Observation window: the last %s of Prometheus history (history_duration %s hours)", hoursText(h), trimFloat(h)))
	} else {
		lines = append(lines, "Observation window: not reported by krr")
	}
	if len(s.Settings) > 0 {
		parts := make([]string, 0, len(s.Settings))
		for _, k := range slices.Sorted(maps.Keys(s.Settings)) {
			parts = append(parts, k+"="+settingText(s.Settings[k]))
		}
		lines = append(lines, "Settings: "+strings.Join(parts, ", "))
	}
	return lines
}

func hoursText(h float64) string {
	if h >= 48 && math.Mod(h, 24) == 0 {
		return trimFloat(h/24) + " days"
	}
	return trimFloat(h) + " hours"
}

func settingText(v any) string {
	switch v := v.(type) {
	case float64:
		return trimFloat(v)
	case string:
		return v
	case nil:
		return "null"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

func trimFloat(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

func krrRow(scan krrScan) ([]string, error) {
	o := scan.Object
	pods := 0
	for _, p := range o.Pods {
		if !p.Deleted {
			pods++
		}
	}
	row := []string{o.Namespace, o.Kind, o.Name, o.Container, strconv.Itoa(pods)}
	var info []string
	// Columns: cpu request, cpu limit, memory request, memory limit.
	for _, res := range krrResources {
		for _, part := range []struct {
			cur, rec map[string]json.RawMessage
		}{{o.Allocations.Requests, scan.Recommended.Requests}, {o.Allocations.Limits, scan.Recommended.Limits}} {
			cur, err := krrValue(part.cur[res], res)
			if err != nil {
				return nil, fmt.Errorf("current %s: %w", res, err)
			}
			rec, err := krrValue(part.rec[res], res)
			if err != nil {
				return nil, fmt.Errorf("recommended %s: %w", res, err)
			}
			row = append(row, cur+" -> "+rec)
		}
		if why := scan.Recommended.Info[res]; why != nil && *why != "" {
			info = append(info, res+": "+*why)
		}
	}
	if o.HPA != nil {
		info = append(info, "has a HorizontalPodAutoscaler")
	}
	severity := scan.Severity
	if severity == "" {
		severity = "-"
	}
	row = append(row, severity, strings.Join(info, "; "))
	return row, nil
}

// krrValue formats one value: a number (cores or bytes), "?" (no data),
// null (unset), or a recommendation object {"value": ..., "severity": ...}.
func krrValue(raw json.RawMessage, resource string) (string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return "unset", nil
	}
	if raw[0] == '{' {
		var rec struct {
			Value json.RawMessage `json:"value"`
		}
		if err := json.Unmarshal(raw, &rec); err != nil {
			return "", err
		}
		return krrValue(rec.Value, resource)
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", err
		}
		if s == "?" {
			return "?", nil
		}
		return "", fmt.Errorf("unexpected value %q", s)
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err != nil {
		return "", fmt.Errorf("unexpected value %s", raw)
	}
	if resource == "memory" {
		return bytesText(f), nil
	}
	return coresText(f), nil
}

// coresText writes CPU cores as kubectl does: millicores below one core.
func coresText(c float64) string {
	if c < 1 {
		return strconv.FormatFloat(math.Round(c*1000), 'f', -1, 64) + "m"
	}
	return strconv.FormatFloat(math.Round(c*100)/100, 'f', -1, 64)
}

// bytesText writes bytes with a binary unit.
func bytesText(b float64) string {
	units := []string{"", "Ki", "Mi", "Gi", "Ti"}
	i := 0
	for b >= 1024 && i < len(units)-1 {
		b /= 1024
		i++
	}
	return strconv.FormatFloat(math.Round(b*10)/10, 'f', -1, 64) + units[i]
}

func krrErrorText(e map[string]any) string {
	if msg, ok := e["message"].(string); ok && msg != "" {
		return msg
	}
	b, err := json.Marshal(e)
	if err != nil {
		return fmt.Sprint(e)
	}
	return string(b)
}
