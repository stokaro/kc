package logview_test

import (
	"testing"

	"github.com/stokaro/kc/internal/logview"
)

func TestQueryMatch(t *testing.T) {
	t.Parallel()
	lines := map[string]string{
		"error":  `{"level":"error","msg":"connection refused","http":{"status":503}}`,
		"info":   `{"level":"info","msg":"GET /healthz","http":{"status":200}}`,
		"warn":   `{"level":"WARN","msg":"slow request","http.status":200}`,
		"text":   `panic: runtime error: index out of range`,
		"broken": `{"level":"error","msg":`,
	}
	tests := []struct {
		query string
		want  []string
	}{
		{query: "", want: []string{"error", "info", "warn", "text", "broken"}},
		{query: "refused", want: []string{"error"}},
		{query: "ERROR", want: []string{"error", "text", "broken"}},
		{query: `"runtime error"`, want: []string{"text"}},
		{query: `/status":(5|2)0[03]/`, want: []string{"error", "info", "warn"}},
		{query: "/connection refused/", want: []string{"error"}},
		{query: "!/^panic: /", want: []string{"error", "info", "warn", "broken"}},
		{query: "/healthz", want: []string{"info"}},
		{query: "level=error", want: []string{"error"}},
		{query: ".level=error", want: []string{"error"}},
		{query: "level=error,warn", want: []string{"error", "warn"}},
		{query: "!level=info", want: []string{"error", "warn", "text", "broken"}},
		{query: "http.status=200", want: []string{"info", "warn"}},
		{query: "http.status~^5", want: []string{"error"}},
		{query: `msg~"^GET /"`, want: []string{"info"}},
		{query: "!healthz", want: []string{"error", "warn", "text", "broken"}},
		{query: "level=error !refused", want: nil},
		{query: "request level=warn", want: []string{"warn"}},
		{query: ".msg", want: []string{"error", "info", "warn", "text", "broken"}},
		{query: "level=error .msg", want: []string{"error"}},
	}
	order := []string{"error", "info", "warn", "text", "broken"}
	for _, tc := range tests {
		t.Run(tc.query, func(t *testing.T) {
			t.Parallel()
			q, err := logview.ParseQuery(tc.query)
			if err != nil {
				t.Fatalf("ParseQuery(%q): %v", tc.query, err)
			}
			var got []string
			for _, name := range order {
				r := logview.NewRecord(lines[name], logview.Source{})
				if q.Match(&r) {
					got = append(got, name)
				}
			}
			if !equalStrings(got, tc.want) {
				t.Fatalf("matched %v, want %v", got, tc.want)
			}
		})
	}
}

func TestQueryNilMatchesAll(t *testing.T) {
	t.Parallel()
	var q *logview.Query
	r := logview.NewRecord("x", logview.Source{})
	if !q.Match(&r) || !q.IsZero() || q.Filters() || q.String() != "" || q.Extract() != nil {
		t.Fatal("a nil query must match everything and do nothing")
	}
}

func TestQueryString(t *testing.T) {
	t.Parallel()
	q := logview.MustParseQuery(`  level=error   "a b"  .msg `)
	if got, want := q.String(), `level=error "a b" .msg`; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	if !q.Filters() || q.IsZero() {
		t.Fatal("the query filters")
	}
	if got := len(q.Extract()); got != 1 {
		t.Fatalf("Extract() has %d paths, want 1", got)
	}
	if only := logview.MustParseQuery(".msg"); only.Filters() || only.IsZero() {
		t.Fatal("an extraction-only query extracts but does not filter")
	}
}

func TestParseQueryErrors(t *testing.T) {
	t.Parallel()
	for _, s := range []string{
		`"unterminated`,
		`/[/`,
		`level~(`,
		`!`,
		`!.msg`,
		`.a..b`,
		`.items[x]`,
		`""`,
		`a..b=1`,
	} {
		t.Run(s, func(t *testing.T) {
			t.Parallel()
			if _, err := logview.ParseQuery(s); err == nil {
				t.Fatalf("ParseQuery(%q) succeeded, want an error", s)
			}
		})
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
