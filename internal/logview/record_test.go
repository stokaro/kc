package logview_test

import (
	"strings"
	"testing"

	"github.com/sttts/kc/internal/logview"
)

func mustPath(t *testing.T, s string) logview.Path {
	t.Helper()
	p, err := logview.ParsePath(s)
	if err != nil {
		t.Fatalf("ParsePath(%q): %v", s, err)
	}
	return p
}

func TestNewRecordDetectsJSON(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		line       string
		wantJSON   bool
		wantPrefix string
	}{
		{name: "object", line: `{"level":"info","msg":"hi"}`, wantJSON: true},
		{name: "spaces around", line: "  {\"a\":1}  ", wantJSON: true},
		{name: "empty object", line: `{}`, wantJSON: true},
		{name: "kubelet timestamp", line: `2026-11-22T10:00:00.123456789Z {"msg":"hi"}`, wantJSON: true, wantPrefix: "2026-11-22T10:00:00.123456789Z"},
		{name: "timestamp with offset", line: `2026-11-22T10:00:00+01:00 {"msg":"hi"}`, wantJSON: true, wantPrefix: "2026-11-22T10:00:00+01:00"},
		{name: "timestamp then text", line: `2026-11-22T10:00:00Z starting up`},
		{name: "plain text", line: `starting server on :8080`},
		{name: "array", line: `[1,2,3]`},
		{name: "string", line: `"just a string"`},
		{name: "truncated object", line: `{"level":"info","msg":"hi"`},
		{name: "trailing garbage", line: `{"a":1} trailing}`},
		{name: "two objects", line: `{"a":1}{"b":2}`},
		{name: "bad value", line: `{"a":tru}`},
		{name: "text in braces", line: `{not json}`},
		{name: "empty", line: ``},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := logview.NewRecord(tc.line, logview.Source{})
			if got := r.IsJSON(); got != tc.wantJSON {
				t.Fatalf("IsJSON() = %v, want %v", got, tc.wantJSON)
			}
			if r.Prefix != tc.wantPrefix {
				t.Fatalf("Prefix = %q, want %q", r.Prefix, tc.wantPrefix)
			}
			if r.Raw != tc.line {
				t.Fatalf("Raw = %q, want the line unchanged", r.Raw)
			}
		})
	}
}

func TestRecordKeepsSource(t *testing.T) {
	t.Parallel()
	src := logview.Source{Pod: "web-1", Container: "app"}
	r := logview.NewRecord(`{"msg":"hi"}`, src)
	if r.Source != src {
		t.Fatalf("Source = %+v, want %+v", r.Source, src)
	}
	if got := src.String(); got != "web-1/app" {
		t.Fatalf("Source.String() = %q", got)
	}
}

func TestRecordField(t *testing.T) {
	t.Parallel()
	line := `{"msg":"hi","http":{"status":200,"path":"/x"},"http.method":"GET",` +
		`"items":[{"name":"a"},{"name":"b"}],"ok":true,"nil":null,"big":12345678901234567890,` +
		`"a b":"spaced","dup":1,"dup":2}`
	r := logview.NewRecord(line, logview.Source{})
	tests := []struct {
		path   string
		want   string
		wantOK bool
	}{
		{path: ".msg", want: "hi", wantOK: true},
		{path: "msg", want: "hi", wantOK: true},
		{path: ".http.status", want: "200", wantOK: true},
		{path: ".http", want: `{"status":200,"path":"/x"}`, wantOK: true},
		{path: ".http.method", want: "GET", wantOK: true},
		{path: ".items[1].name", want: "b", wantOK: true},
		{path: ".items[5].name"},
		{path: ".items", want: `[{"name":"a"},{"name":"b"}]`, wantOK: true},
		{path: ".ok", want: "true", wantOK: true},
		{path: ".nil", want: "null", wantOK: true},
		{path: ".big", want: "12345678901234567890", wantOK: true},
		{path: `.["a b"]`, want: "spaced", wantOK: true},
		{path: ".dup", want: "2", wantOK: true},
		{path: ".missing"},
		{path: ".msg.deeper"},
		// The line is compact JSON, so the whole record reads back unchanged.
		{path: ".", want: line, wantOK: true},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			t.Parallel()
			got, ok := r.Field(mustPath(t, tc.path))
			if ok != tc.wantOK || got != tc.want {
				t.Fatalf("Field(%s) = %q, %v; want %q, %v", tc.path, got, ok, tc.want, tc.wantOK)
			}
		})
	}
	text := logview.NewRecord("plain", logview.Source{})
	if _, ok := text.Field(mustPath(t, ".msg")); ok {
		t.Fatal("a text record has no fields")
	}
}

func TestParsePathErrors(t *testing.T) {
	t.Parallel()
	for _, s := range []string{"", "..a", ".a.", ".a..b", ".a[", ".a[x]", ".a[-1]", `.a["x]`, ".a b", `.a[0]b`, ".a.[0]"} {
		t.Run(s, func(t *testing.T) {
			t.Parallel()
			if _, err := logview.ParsePath(s); err == nil {
				t.Fatalf("ParsePath(%q) succeeded, want an error", s)
			}
		})
	}
}

func TestNewRecordLargeLine(t *testing.T) {
	t.Parallel()
	big := `{"msg":"` + strings.Repeat("x", logview.MaxParseBytes) + `"}`
	r := logview.NewRecord(big, logview.Source{})
	if r.IsJSON() {
		t.Fatal("a line over MaxParseBytes must not be parsed")
	}
	if r.Raw != big {
		t.Fatal("the large line must be kept unchanged")
	}
	f := logview.DefaultFormat()
	if got := f.Formatted(&r, nil); len(got) != len(big) {
		t.Fatalf("formatted large line has %d bytes, want %d", len(got), len(big))
	}
}

func TestNewRecordDeepNesting(t *testing.T) {
	t.Parallel()
	deep := `{"a":` + strings.Repeat("[", 5000) + strings.Repeat("]", 5000) + `}`
	r := logview.NewRecord(deep, logview.Source{})
	if r.IsJSON() {
		t.Fatal("deeply nested JSON must stay text")
	}
}

func TestNewRecordInvalidUTF8(t *testing.T) {
	t.Parallel()
	line := "{\"msg\":\"bad \xff\xfe byte\",\"k\":\"\xc3\"}"
	r := logview.NewRecord(line, logview.Source{})
	if !r.IsJSON() {
		t.Fatal("invalid UTF-8 inside a string must not stop detection")
	}
	if r.Raw != line {
		t.Fatal("Raw must keep the original bytes")
	}
	got := logview.Format{Columns: logview.DefaultFormat().Columns}.Formatted(&r, nil)
	if !strings.Contains(got, "bad �� byte") {
		t.Fatalf("formatted = %q, want replacement characters", got)
	}
	if !isValidUTF8(got) {
		t.Fatalf("formatted line is not valid UTF-8: %q", got)
	}
	text := logview.NewRecord("text \xff line", logview.Source{})
	if got := logview.DefaultFormat().Raw(&text); !isValidUTF8(got) {
		t.Fatalf("raw display is not valid UTF-8: %q", got)
	}
}

// FuzzFormatted checks that any line gives valid UTF-8 in both views, that a
// JSON record stays on one line, and that the original is kept.
func FuzzFormatted(f *testing.F) {
	for _, seed := range []string{
		`{"level":"info","msg":"hi"}`,
		"{\"msg\":\"a\\nb\\u001b[2J\"}",
		"2026-11-22T10:00:00Z {\"a\":{\"b\":[1,{\"c\":null}]}}",
		"text \xff\xfe",
		`{"a":`,
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, line string) {
		r := logview.NewRecord(line, logview.Source{})
		if r.Raw != line {
			t.Fatal("Raw changed")
		}
		format := logview.DefaultFormat()
		for _, got := range []string{format.Formatted(&r, nil), format.Raw(&r)} {
			if !isValidUTF8(got) {
				t.Fatalf("display %q is not valid UTF-8", got)
			}
		}
		if r.IsJSON() {
			if got := format.Formatted(&r, nil); strings.ContainsAny(got, "\n\r") {
				t.Fatalf("formatted JSON record %q spans lines", got)
			}
		}
	})
}
