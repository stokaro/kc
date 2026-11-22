package ui

import (
	"errors"
	"strings"
	"testing"

	kccluster "github.com/sttts/kc/internal/cluster"
	"github.com/sttts/kc/internal/models"
	"github.com/sttts/kc/pkg/appconfig"
)

// Read-only mode refuses shell and attach: kc cannot see what is typed in
// the session. A writable context, or the panel's own writable context,
// still opens one.
func TestExecGateRefusesReadOnlyContexts(t *testing.T) {
	app := NewApp()
	app.cfg.ReadOnly = appconfig.ReadOnlyConfig{Contexts: []string{"*prod*"}}
	target := func(context string) models.ExecTarget {
		return models.ExecTarget{Cluster: kccluster.Key{ContextName: context}, Namespace: "shop", Pod: "web-0", Container: "app"}
	}
	cases := []struct {
		name     string
		context  string
		override ReadOnlyOverride
		mode     models.ExecMode
		refused  bool
	}{
		{"shell in a read-only context", "eu-prod", ReadOnlyFromConfig, models.ExecShell, true},
		{"attach in a read-only context", "eu-prod", ReadOnlyFromConfig, models.ExecAttach, true},
		{"shell in a writable context", "dev", ReadOnlyFromConfig, models.ExecShell, false},
		{"--readonly refuses every context", "dev", ReadOnlyForced, models.ExecAttach, true},
		{"--write allows a matching context", "eu-prod", ReadOnlyWriteForced, models.ExecShell, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app.readOnlyOverride = tc.override
			err := app.execGate(target(tc.context), tc.mode)
			if got := errors.Is(err, errExecReadOnly); got != tc.refused {
				t.Fatalf("execGate refused = %v (err %v), want %v", got, err, tc.refused)
			}
		})
	}
}

// A refused session shows the reason and never starts kubectl.
func TestExecResolvedInReadOnlyShowsTheReason(t *testing.T) {
	app := NewApp()
	app.readOnlyOverride = ReadOnlyForced
	msg := execResolvedMsg{target: models.ExecTarget{Cluster: kccluster.Key{ContextName: "dev"}, Namespace: "shop", Pod: "web-0", Container: "app"}, mode: models.ExecShell}
	app.toastLogger = nil
	cmd := app.handleExecResolved(msg)
	if cmd == nil {
		t.Fatal("a refused session must show a message")
	}
	toast, ok := cmd().(showToastMsg)
	if !ok {
		t.Fatalf("a refused session must show a toast, not start kubectl")
	}
	if want := "Shell in "; !strings.HasPrefix(toast.text, want) || !strings.Contains(toast.text, "read-only mode") {
		t.Fatalf("toast = %q, want the shell refusal", toast.text)
	}
}
