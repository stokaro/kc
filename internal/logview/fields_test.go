package logview_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/stokaro/kc/internal/logview"
)

func TestRecordFields(t *testing.T) {
	t.Parallel()
	r := logview.NewRecord(`{"msg":"hi","http":{"status":200,"h":{"x-id":"7"}},"tags":["a"],"empty":{},"a.b":true}`, logview.Source{})
	var got []string
	for _, f := range r.Fields() {
		got = append(got, f.Path.String()+"="+f.Value)
	}
	want := []string{`.msg=hi`, `.http.status=200`, `.http.h.x-id=7`, `.tags=["a"]`, `.empty={}`, `.["a.b"]=true`}
	if !equalStrings(got, want) {
		t.Fatalf("Fields() = %q, want %q", got, want)
	}
	text := logview.NewRecord("plain text", logview.Source{})
	if f := text.Fields(); f != nil {
		t.Fatalf("a text record has fields %v", f)
	}
}

// TestFieldTermIsExact builds a filter from each field value and checks
// that it keeps the record and refuses near misses: other case, a value
// that is a part of a comma list, a regex that would match.
func TestFieldTermIsExact(t *testing.T) {
	t.Parallel()
	values := []string{
		`abc`, `a,b`, `x y`, `say "hi"`, `back\slash`, `end\`, `a\"b`, `.*`, `/re/`, `!not`, `a=b~c`, ``, `ünï`, "tab\there",
	}
	keys := [][]string{{"id"}, {"req", "id"}, {"key with space"}, {`we"ird]`}, {"a.b"}, {"0"}}
	for _, k := range keys {
		for _, v := range values {
			t.Run(fmt.Sprintf("%q=%q", k, v), func(t *testing.T) {
				t.Parallel()
				line := jsonAt(k, v)
				r := logview.NewRecord(line, logview.Source{})
				path := logview.KeyPath(k...)
				if got, ok := r.Field(path); !ok || got != v {
					t.Fatalf("KeyPath %s on %s = %q, %v", path, line, got, ok)
				}
				include := logview.FieldTerm(path, v, false)
				q, err := logview.ParseQuery(include)
				if err != nil {
					t.Fatalf("ParseQuery(%s): %v", include, err)
				}
				if !q.Match(&r) {
					t.Fatalf("%s does not keep %s", include, line)
				}
				for _, other := range []string{v + "x", "X" + v, strings.ToUpper(v), "a", "b", "anything"} {
					if other == v {
						continue
					}
					o := logview.NewRecord(jsonAt(k, other), logview.Source{})
					if q.Match(&o) {
						t.Errorf("%s keeps %s", include, jsonAt(k, other))
					}
				}
				missing := logview.NewRecord(`{"other":1}`, logview.Source{})
				textLine := logview.NewRecord(v, logview.Source{})
				if q.Match(&missing) || q.Match(&textLine) {
					t.Error("an exact term matches a record without the field")
				}
				exclude, err := logview.ParseQuery(logview.FieldTerm(path, v, true))
				if err != nil {
					t.Fatal(err)
				}
				if exclude.Match(&r) || !exclude.Match(&missing) {
					t.Error("the exclude term is not the opposite of the include term")
				}
			})
		}
	}
}

// jsonAt is {"k0":{"k1":...value}} with value as a JSON string.
func jsonAt(keys []string, value string) string {
	s := quoteJSON(value)
	for _, k := range slices.Backward(keys) {
		s = "{" + quoteJSON(k) + ":" + s + "}"
	}
	return s
}

func quoteJSON(s string) string {
	b := []byte{'"'}
	for _, c := range []byte(s) {
		switch c {
		case '"', '\\':
			b = append(b, '\\', c)
		case '\t':
			b = append(b, '\\', 't')
		default:
			b = append(b, c)
		}
	}
	return string(append(b, '"'))
}

func TestQuotedStepsDoNotJoin(t *testing.T) {
	t.Parallel()
	r := logview.NewRecord(`{"a.b":1,"a":{"b":2}}`, logview.Source{})
	for path, want := range map[string]string{`.["a"]["b"]`: "2", `.["a"].b`: "2", `.["a.b"]`: "1", `.a.b`: "1"} {
		p, err := logview.ParsePath(path)
		if err != nil {
			t.Fatal(err)
		}
		if got, ok := r.Field(p); !ok || got != want {
			t.Errorf("%s = %q, %v; want %s", path, got, ok, want)
		}
	}
	q := logview.MustParseQuery(`level=ERROR,warn`)
	warn := logview.NewRecord(`{"level":"WARN"}`, logview.Source{})
	if !q.Match(&warn) {
		t.Fatal("= keeps matching any case and comma lists")
	}
}

func newContextLog(t *testing.T, limits logview.Limits) *logview.Log {
	t.Helper()
	l := logview.NewLog(logview.DefaultFormat())
	l.SetLimits(limits)
	a, b := logview.Source{Pod: "p", Container: "a"}, logview.Source{Pod: "p", Container: "b"}
	for i := range 10 {
		src := a
		if i%2 == 1 {
			src = b
		}
		l.Append(src, fmt.Sprintf("line %d", i))
	}
	return l
}

func contextTexts(lines []logview.ContextLine) []string {
	var out []string
	for _, l := range lines {
		mark := map[logview.ContextKind]string{logview.ContextAround: " ", logview.ContextMatch: "*", logview.ContextAnchor: ">"}[l.Kind]
		out = append(out, mark+l.Text)
	}
	return out
}

func TestLogContext(t *testing.T) {
	t.Parallel()
	l := newContextLog(t, logview.Limits{})
	l.SetQuery(logview.MustParseQuery(`/line [46]/`))
	seq, ok := l.SeqAt(0) // line 4
	if !ok {
		t.Fatal("no first shown line")
	}
	if line, ok := l.DisplayLine(seq); !ok || line != 0 {
		t.Fatalf("DisplayLine = %d, %v", line, ok)
	}

	merged, complete, err := l.Context(seq, 2, 2, false)
	if err != nil || !complete {
		t.Fatalf("Context: %v, complete %v", err, complete)
	}
	if got, want := contextTexts(merged), []string{" [p/a] line 2", " [p/b] line 3", ">[p/a] line 4", " [p/b] line 5", "*[p/a] line 6"}; !equalStrings(got, want) {
		t.Fatalf("merged context = %q, want %q", got, want)
	}
	same, _, err := l.Context(seq, 2, 2, true)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := contextTexts(same), []string{" [p/a] line 0", " [p/a] line 2", ">[p/a] line 4", "*[p/a] line 6", " [p/a] line 8"}; !equalStrings(got, want) {
		t.Fatalf("same-source context = %q, want %q", got, want)
	}
	// At the edges the context is shorter.
	last, _ := l.SeqAt(1) // line 6
	tail, _, _ := l.Context(last+3, 1, 5, false)
	if got, want := contextTexts(tail), []string{" [p/a] line 8", ">[p/b] line 9"}; !equalStrings(got, want) {
		t.Fatalf("context at the end = %q, want %q", got, want)
	}
}

func TestLogContextAfterDrops(t *testing.T) {
	t.Parallel()
	l := newContextLog(t, logview.Limits{MaxRecords: 4})
	first := l.Records()[0]
	if first.Raw == "line 0" {
		t.Fatal("the limit did not drop anything")
	}
	if _, err := l.Record(0); !errors.Is(err, logview.ErrDropped) {
		t.Fatalf("Record(0) = %v, want ErrDropped", err)
	}
	if _, _, err := l.Context(0, 1, 1, false); !errors.Is(err, logview.ErrDropped) {
		t.Fatalf("Context of a dropped record: %v", err)
	}
	seq, _ := l.SeqAt(0)
	r, err := l.Record(seq)
	if err != nil || r.Raw != first.Raw {
		t.Fatalf("Record(%d) = %v, %v; want %q", seq, r, err, first.Raw)
	}
	lines, complete, err := l.Context(seq, 3, 0, false)
	if err != nil || complete || len(lines) != 1 {
		t.Fatalf("context at the oldest kept line: %q, complete %v, %v", contextTexts(lines), complete, err)
	}
}
