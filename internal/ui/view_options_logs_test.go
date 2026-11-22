package ui

import (
	"reflect"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/pkg/appconfig"
)

func newLogsOptionsModel(cfg ViewOptionsLogsConfig) *ViewOptionsModel {
	return NewViewOptionsModel(ViewOptionsConfig{
		PanelIndex:       -1,
		SkipPanelSection: true,
		Viewer:           &ViewOptionsViewerConfig{ThemeNames: []string{"dracula"}, Theme: "dracula", WrapMode: "scroll"},
		Logs:             &cfg,
	})
}

// focusOption moves the focus to opt and fails when the dialog lacks it.
func focusOption(t *testing.T, m *ViewOptionsModel, opt viewOptionKind) {
	t.Helper()
	for i, e := range m.entries {
		if e.kind == viewOptionEntryOption && e.option == opt {
			m.focus = i
			return
		}
	}
	t.Fatalf("option %d not in the dialog", opt)
}

func TestViewOptionsLogsSectionRoundTrips(t *testing.T) {
	t.Parallel()
	in := ViewOptionsLogsConfig{TailLines: 200, SinceSeconds: 0, Timestamps: false, Previous: true}
	m := newLogsOptionsModel(in)
	msg := m.commit(true, false)
	if msg.Logs == nil {
		t.Fatal("commit has no Logs payload")
	}
	want := ViewOptionsLogsPayload{TailLines: 200, Previous: true}
	if *msg.Logs != want {
		t.Fatalf("payload = %+v, want %+v", *msg.Logs, want)
	}

	focusOption(t, m, viewOptionLogsTail)
	m.adjustCurrent(1)
	focusOption(t, m, viewOptionLogsSince)
	m.adjustCurrent(-1) // wraps from Off to the longest window
	focusOption(t, m, viewOptionLogsTimestamps)
	m.adjustCurrent(0)
	focusOption(t, m, viewOptionLogsPrevious)
	m.adjustCurrent(0)
	msg = m.commit(true, false)
	want = ViewOptionsLogsPayload{TailLines: 500, SinceSeconds: 3600, Timestamps: true}
	if *msg.Logs != want {
		t.Fatalf("payload after edits = %+v, want %+v", *msg.Logs, want)
	}

	labels := map[viewOptionKind][2]string{
		viewOptionLogsTail:       {"Tail lines", "500"},
		viewOptionLogsSince:      {"Since", "1h"},
		viewOptionLogsTimestamps: {"Timestamps", "On"},
		viewOptionLogsPrevious:   {"Previous", "Off"},
	}
	for opt, wantLV := range labels {
		if l, v := m.optionLabelAndValue(opt); l != wantLV[0] || v != wantLV[1] {
			t.Errorf("option %d shows %q: %q, want %q: %q", opt, l, v, wantLV[0], wantLV[1])
		}
	}
}

func TestViewOptionsWithoutLogsHasNoLogsPayload(t *testing.T) {
	t.Parallel()
	m := NewViewOptionsModel(ViewOptionsConfig{
		PanelIndex: -1, SkipPanelSection: true,
		Viewer: &ViewOptionsViewerConfig{ThemeNames: []string{"dracula"}, Theme: "dracula"},
	})
	if msg := m.commit(true, false); msg.Logs != nil {
		t.Fatalf("text viewer dialog carries log options: %+v", msg.Logs)
	}
}

func TestViewOptionsKeepsAnUnlistedConfiguredValue(t *testing.T) {
	t.Parallel()
	m := newLogsOptionsModel(ViewOptionsLogsConfig{TailLines: 300, SinceSeconds: 7200})
	if l, v := m.optionLabelAndValue(viewOptionLogsTail); v != "300" {
		t.Fatalf("%s shows %q, want 300", l, v)
	}
	if msg := m.commit(true, false); msg.Logs.TailLines != 300 || msg.Logs.SinceSeconds != 7200 {
		t.Fatalf("payload = %+v, want the configured values kept", *msg.Logs)
	}
	focusOption(t, m, viewOptionLogsTail)
	m.adjustCurrent(1)
	if msg := m.commit(true, false); msg.Logs.TailLines != 500 {
		t.Fatalf("next after 300 = %d, want 500", msg.Logs.TailLines)
	}
}

func TestWithChoice(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		current int64
		want    []int64
		wantIdx int
	}{
		{"present", 200, []int64{100, 200, -1}, 1},
		{"between", 150, []int64{100, 150, 200, -1}, 1},
		{"above all listed", 900, []int64{100, 200, 900, -1}, 2},
		{"below all", 10, []int64{10, 100, 200, -1}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, idx := withChoice([]int64{100, 200, -1}, tt.current)
			if !reflect.DeepEqual(got, tt.want) || idx != tt.wantIdx {
				t.Fatalf("withChoice = %v, %d; want %v, %d", got, idx, tt.want, tt.wantIdx)
			}
		})
	}
}

func TestLogsSpecWithDefaults(t *testing.T) {
	t.Parallel()
	row := models.LogsSpec{Namespace: "web", Pod: "p", Container: "app", TailLines: 200, Previous: true}
	tests := []struct {
		name string
		cfg  appconfig.LogsViewerConfig
		want models.LogsSpec
	}{
		{
			name: "defaults keep the row",
			cfg:  appconfig.Default().Viewer.Logs,
			want: row,
		},
		{
			name: "configured values apply, previous stays from the row",
			cfg:  appconfig.LogsViewerConfig{Tail: -1, Since: metav1.Duration{Duration: 15 * time.Minute}, Timestamps: true},
			want: models.LogsSpec{Namespace: "web", Pod: "p", Container: "app", TailLines: -1, SinceSeconds: 900, Timestamps: true, Previous: true},
		},
		{
			name: "zero tail keeps the row's tail",
			cfg:  appconfig.LogsViewerConfig{},
			want: row,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := logsSpecWithDefaults(row, tt.cfg); got != tt.want {
				t.Fatalf("logsSpecWithDefaults = %+v, want %+v", got, tt.want)
			}
		})
	}
}
