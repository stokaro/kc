package diagnostic_test

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/stokaro/kc/internal/diagnostic"
	"github.com/stokaro/kc/pkg/appconfig"
)

func readFixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/krr-simple.json")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestParseKRRFixture(t *testing.T) {
	t.Parallel()
	rep, err := diagnostic.ParseKRR(readFixture(t), nil)
	if err != nil {
		t.Fatalf("ParseKRR: %v", err)
	}
	want := [][]string{
		{"prod", "Deployment", "web", "app", "2", "100m -> 250m", "unset -> unset", "128Mi -> 100Mi", "256Mi -> 100Mi", "WARNING", ""},
		{"prod", "Deployment", "web", "sidecar", "2", "1.5 -> ?", "2 -> ?", "2Gi -> ?", "2Gi -> ?", "UNKNOWN", "cpu: Not enough data; memory: Not enough data; has a HorizontalPodAutoscaler"},
		{"prod", "StatefulSet", "db", "postgres", "1", "500m -> 200m", "1 -> unset", "1Gi -> 768Mi", "1Gi -> 768Mi", "OK", ""},
	}
	// Rows sort by namespace, kind, name, container.
	slices.SortStableFunc(want, func(a, b []string) int {
		return strings.Compare(strings.Join(a[:4], "\x00"), strings.Join(b[:4], "\x00"))
	})
	if len(rep.Rows) != len(want) {
		t.Fatalf("rows = %d, want %d: %q", len(rep.Rows), len(want), rep.Rows)
	}
	for i := range want {
		if !slices.Equal(rep.Rows[i], want[i]) {
			t.Errorf("row %d = %q\nwant      %q", i, rep.Rows[i], want[i])
		}
	}
	text := rep.Text()
	for _, s := range []string{
		"Source: krr",
		"Strategy: simple",
		"Observation window: the last 14 days of Prometheus history (history_duration 336 hours)",
		"allow_hpa=false",
		"cpu_percentile=95",
		"Scans: 3 shown, 0 outside the scope.",
		"never changes requests or limits",
	} {
		if !strings.Contains(text, s) {
			t.Errorf("report lacks %q:\n%s", s, text)
		}
	}
}

func TestParseKRRScope(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		scopes  []diagnostic.Scope
		rows    int
		outside int
	}{
		{name: "workload", scopes: []diagnostic.Scope{{Namespace: "prod", Kind: "Deployment", Name: "web"}}, rows: 2, outside: 1},
		{name: "kind case", scopes: []diagnostic.Scope{{Namespace: "prod", Kind: "statefulset", Name: "db"}}, rows: 1, outside: 2},
		{name: "same name other kind", scopes: []diagnostic.Scope{{Namespace: "prod", Kind: "StatefulSet", Name: "web"}}, rows: 0, outside: 3},
		{name: "namespace", scopes: []diagnostic.Scope{{Namespace: "prod"}}, rows: 3},
		{name: "other namespace", scopes: []diagnostic.Scope{{Namespace: "stage"}}, rows: 0, outside: 3},
		{name: "two workloads", scopes: []diagnostic.Scope{{Namespace: "prod", Kind: "Deployment", Name: "web"}, {Namespace: "prod", Kind: "StatefulSet", Name: "db"}}, rows: 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rep, err := diagnostic.ParseKRR(readFixture(t), tc.scopes)
			if err != nil {
				t.Fatal(err)
			}
			if len(rep.Rows) != tc.rows {
				t.Errorf("rows = %d, want %d", len(rep.Rows), tc.rows)
			}
			text := rep.Text()
			if want := fmt.Sprintf("Scans: %d shown, %d outside the scope.", tc.rows, tc.outside); !strings.Contains(text, want) {
				t.Errorf("report lacks %q", want)
			}
			if !strings.Contains(text, "Scope: ") {
				t.Errorf("a scoped report names its scope:\n%s", text)
			}
			if tc.rows == 0 && !strings.Contains(text, "No rows.") {
				t.Errorf("an empty scope says so:\n%s", text)
			}
			for _, row := range rep.Rows {
				if !slices.ContainsFunc(tc.scopes, func(s diagnostic.Scope) bool { return s.Namespace == row[0] }) {
					t.Errorf("row %q is outside the scope", row)
				}
			}
		})
	}
}

func TestParseKRRRejectsMalformedOutput(t *testing.T) {
	t.Parallel()
	valid := string(readFixture(t))
	tests := []struct {
		name, data, want string
	}{
		{name: "empty", data: "", want: "not JSON"},
		{name: "table output", data: "Running Simple Strategy\n| Number | Namespace |", want: "not JSON"},
		{name: "two documents", data: `{"scans": [], "strategy": {"name": "simple"}} {}`, want: "more than one"},
		{name: "no scans", data: `{"strategy": {"name": "simple", "settings": {}}}`, want: "not supported"},
		{name: "no strategy", data: `{"scans": []}`, want: "not supported"},
		{name: "yaml-ish", data: `[1, 2]`, want: "not JSON"},
		{name: "scan without object", data: `{"scans": [{"recommended": {}}], "strategy": {"name": "simple"}}`, want: "scan 0"},
		{name: "bad value", data: strings.Replace(valid, `"cpu": 0.1`, `"cpu": "lots"`, 1), want: "unexpected value"},
		{name: "value of wrong type", data: strings.Replace(valid, `"cpu": 0.1`, `"cpu": [1]`, 1), want: "unexpected value"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rep, err := diagnostic.ParseKRR([]byte(tc.data), nil)
			if err == nil {
				t.Fatalf("ParseKRR accepted malformed output, report %+v", rep)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestParseKRREmptyScansIsAReport(t *testing.T) {
	t.Parallel()
	rep, err := diagnostic.ParseKRR([]byte(`{"scans": [], "strategy": {"name": "simple", "settings": {}}, "errors": [{"message": "Prometheus not found"}]}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	text := rep.Text()
	for _, want := range []string{"No rows.", "Errors reported by krr:", "Prometheus not found", "Observation window: not reported by krr"} {
		if !strings.Contains(text, want) {
			t.Errorf("report lacks %q:\n%s", want, text)
		}
	}
}

func TestReportTextSanitizesToolText(t *testing.T) {
	t.Parallel()
	data := strings.Replace(string(readFixture(t)), `"container": "app"`, `"container": "app\u001b]8;;https://evil.example\u0007click\u001b]8;;\u0007\u001b[2J"`, 1)
	rep, err := diagnostic.ParseKRR([]byte(data), nil)
	if err != nil {
		t.Fatal(err)
	}
	text := rep.Text()
	if strings.ContainsAny(text, "\x1b\x07") {
		t.Fatalf("report keeps terminal control characters: %q", text)
	}
	if !strings.Contains(text, "app?]8;;https://evil.example?click") {
		t.Fatalf("control characters must show as ?, got:\n%s", text)
	}
}

func TestSanitize(t *testing.T) {
	t.Parallel()
	tests := []struct{ in, want string }{
		{"plain", "plain"},
		{"line\nnext\ttab", "line\nnext\ttab"},
		{"\x1b[31mred", "?[31mred"},
		{"cr\rover", "cr?over"},
		{"bidi\u202eevil", "bidi?evil"},
		{"bad\xffutf8", "bad?utf8"},
	}
	for _, tc := range tests {
		if got := diagnostic.Sanitize(tc.in); got != tc.want {
			t.Errorf("Sanitize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Every adapter the configuration accepts has a parser.
func TestEveryConfiguredAdapterHasAParser(t *testing.T) {
	t.Parallel()
	for _, name := range appconfig.DiagnosticAdapters {
		if _, ok := diagnostic.Lookup(name); !ok {
			t.Errorf("adapter %q has no parser", name)
		}
	}
	if _, ok := diagnostic.Lookup("gadget"); ok {
		t.Errorf("unknown adapters must not resolve")
	}
}
