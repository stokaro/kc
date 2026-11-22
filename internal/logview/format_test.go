package logview_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/sttts/kc/internal/logview"
)

func isValidUTF8(s string) bool { return utf8.ValidString(s) }

// plain is the default format without colors, so expectations stay readable.
func plain() logview.Format {
	f := logview.DefaultFormat()
	f.Color = false
	return f
}

func TestFormatted(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		line    string
		src     logview.Source
		extract string
		want    string
	}{
		{
			name: "slog record",
			line: `{"time":"2026-11-22T10:00:00Z","level":"INFO","msg":"listening","addr":":8080"}`,
			want: "2026-11-22T10:00:00Z INFO  listening addr=:8080",
		},
		{
			name: "column order wins over key order",
			line: `{"msg":"done","user":"bob","level":"error","ts":1732269600.5}`,
			want: "1732269600.5 ERROR done user=bob",
		},
		{
			name: "pino numeric level",
			line: `{"level":40,"time":1,"msg":"slow"}`,
			want: "1 WARN  slow",
		},
		{
			name: "nested objects are flattened",
			line: `{"msg":"req","http":{"method":"GET","status":200},"tags":["a","b"],"empty":{}}`,
			want: `req http.method=GET http.status=200 tags=["a","b"] empty={}`,
		},
		{
			name: "values with spaces and quotes are quoted",
			line: `{"msg":"x","err":"connection refused","q":"say \"hi\"","blank":"","eq":"a=b"}`,
			want: `x err="connection refused" q="say \"hi\"" blank="" eq="a=b"`,
		},
		{
			name: "control characters are escaped",
			line: `{"msg":"line1\nline2\u001b[31m","k":"tab\there"}`,
			want: `line1\nline2\x1b[31m k="tab\there"`,
		},
		{
			name: "no known columns",
			line: `{"a":1,"b":null,"c":true}`,
			want: "a=1 b=null c=true",
		},
		{
			name: "text line stays as it is",
			line: `plain text line`,
			want: "plain text line",
		},
		{
			name: "broken JSON stays visible",
			line: `{"msg":"cut`,
			want: `{"msg":"cut`,
		},
		{
			name: "kubelet timestamp kept in front",
			line: `2026-11-22T10:00:00.5Z {"level":"debug","msg":"tick"}`,
			want: "2026-11-22T10:00:00.5Z DEBUG tick",
		},
		{
			name: "source in front",
			line: `{"msg":"hi"}`,
			src:  logview.Source{Pod: "web-1", Container: "app"},
			want: "[web-1/app] hi",
		},
		{
			name:    "extract one field",
			line:    `{"time":"t","level":"info","msg":"hello","user":"bob"}`,
			extract: ".msg",
			want:    "hello",
		},
		{
			name:    "extract several fields",
			line:    `{"msg":"hello","http":{"status":503}}`,
			extract: ".http.status .msg",
			want:    "503 hello",
		},
		{
			name:    "extract a missing field shows the whole record",
			line:    `{"msg":"hello"}`,
			extract: ".nothere",
			want:    "hello",
		},
		{
			name:    "extract does not touch text lines",
			line:    `panic: boom`,
			extract: ".msg",
			want:    "panic: boom",
		},
		{
			name:    "extract an object",
			line:    `{"req":{"id":7,"path":"/"}}`,
			extract: ".req",
			want:    `{"id":7,"path":"/"}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			q := logview.MustParseQuery(tc.extract)
			r := logview.NewRecord(tc.line, tc.src)
			if got := plain().Formatted(&r, q.Extract()); got != tc.want {
				t.Fatalf("Formatted() =\n %q\nwant\n %q", got, tc.want)
			}
		})
	}
}

func TestFormattedCustomColumns(t *testing.T) {
	t.Parallel()
	f := logview.Format{Columns: []logview.Column{
		{Kind: logview.ColumnMessage, Keys: []string{"message"}},
		{Kind: logview.ColumnField, Keys: []string{"logger"}},
	}}
	r := logview.NewRecord(`{"level":"info","logger":"db","message":"up"}`, logview.Source{})
	if got, want := f.Formatted(&r, nil), "up db level=info"; got != want {
		t.Fatalf("Formatted() = %q, want %q", got, want)
	}
}

func TestFormattedLevelColors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		level string
		color string
	}{
		{level: "error", color: "\x1b[31mERROR"},
		{level: "WARNING", color: "\x1b[33mWARNING"},
		{level: "info", color: "\x1b[32mINFO "},
		{level: "debug", color: "\x1b[36mDEBUG"},
		{level: "fatal", color: "\x1b[91mFATAL"},
		{level: "60", color: "\x1b[91mFATAL"},
	}
	for _, tc := range tests {
		t.Run(tc.level, func(t *testing.T) {
			t.Parallel()
			r := logview.NewRecord(`{"level":"`+tc.level+`","msg":"m"}`, logview.Source{})
			got := logview.DefaultFormat().Formatted(&r, nil)
			// Only the foreground is reset, so the viewer's background stays.
			if !strings.Contains(got, tc.color+"\x1b[39m") || strings.Contains(got, "\x1b[0m") {
				t.Fatalf("Formatted() = %q, want %q", got, tc.color)
			}
		})
	}
	r := logview.NewRecord(`{"level":"custom","msg":"m"}`, logview.Source{})
	if got := logview.DefaultFormat().Formatted(&r, nil); got != "CUSTOM m" {
		t.Fatalf("an unknown level is shown without color, got %q", got)
	}
}

func TestRaw(t *testing.T) {
	t.Parallel()
	line := `{"level":"info","msg":"hi"}`
	r := logview.NewRecord(line, logview.Source{})
	if got := plain().Raw(&r); got != line {
		t.Fatalf("Raw() = %q, want the original line", got)
	}
	r = logview.NewRecord(line, logview.Source{Pod: "p"})
	if got := plain().Raw(&r); got != "[p] "+line {
		t.Fatalf("Raw() = %q, want the source in front", got)
	}
}
