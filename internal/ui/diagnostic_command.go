package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/stokaro/kc/internal/diagnostic"
	"github.com/stokaro/kc/internal/execctx"
	"github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/pkg/appconfig"
)

// A diagnostic command (a custom command with `diagnostic:`) runs a local
// tool once, without a shell, and shows its parsed output in a report
// viewer. See docs/adr/0198-diagnostic-commands.md.

const (
	diagnosticReportModal = "diagnostic_report"
	diagnosticStopModal   = "diagnostic_stop"
)

// diagnosticRuns are the running diagnostics by command name. A command
// runs at most once at a time; picking it again offers to stop it.
type diagnosticRuns struct {
	seq  int
	runs map[string]*diagnosticRun
}

// diagnosticRun is what a run was started with. It never changes, so a
// result that arrives after the user moved on still names its own scope.
type diagnosticRun struct {
	id      int
	cmd     appconfig.CommandConfig
	argv    []string
	context string
	ns      string
	scopes  []diagnostic.Scope
	slot    string
	started time.Time
	cancel  context.CancelFunc
	release func()
}

// diagnosticDoneMsg carries the result of a run back to the update loop.
type diagnosticDoneMsg struct {
	run *diagnosticRun
	res diagnostic.Result
}

// workloadKinds maps workload resources to the kind tools such as KRR
// report.
var workloadKinds = map[schema.GroupResource]string{
	{Group: "apps", Resource: "deployments"}:                    "Deployment",
	{Group: "apps", Resource: "statefulsets"}:                   "StatefulSet",
	{Group: "apps", Resource: "daemonsets"}:                     "DaemonSet",
	{Group: "batch", Resource: "jobs"}:                          "Job",
	{Group: "batch", Resource: "cronjobs"}:                      "CronJob",
	{Group: "argoproj.io", Resource: "rollouts"}:                "Rollout",
	{Group: "apps.openshift.io", Resource: "deploymentconfigs"}: "DeploymentConfig",
}

// diagnosticScopes limits a report to the targets: a workload to itself,
// a namespace or another namespaced object to its namespace. No targets,
// or a cluster-scoped one, set no limit.
func diagnosticScopes(items []models.Item) []diagnostic.Scope {
	var out []diagnostic.Scope
	for _, it := range items {
		obj, ok := it.(models.ObjectItem)
		if !ok {
			if n, ok := it.(interface{ Namespace() string }); ok && n.Namespace() != "" {
				out = append(out, diagnostic.Scope{Namespace: n.Namespace()})
			}
			continue
		}
		gvr := obj.GVR()
		switch {
		case gvr.Group == "" && gvr.Resource == "namespaces":
			out = append(out, diagnostic.Scope{Namespace: obj.Name()})
		case workloadKinds[gvr.GroupResource()] != "":
			out = append(out, diagnostic.Scope{Namespace: obj.Namespace(), Kind: workloadKinds[gvr.GroupResource()], Name: obj.Name()})
		case obj.Namespace() != "":
			out = append(out, diagnostic.Scope{Namespace: obj.Namespace()})
		default:
			return nil
		}
	}
	return out
}

// diagnosticNamespace is the namespace of the first target, or the name
// of a namespace target.
func diagnosticNamespace(items []models.Item) string {
	for _, s := range diagnosticScopes(items) {
		if s.Namespace != "" {
			return s.Namespace
		}
	}
	return ""
}

// startDiagnostic runs cmd for items from panel p, or offers to stop the
// run of cmd that is still going.
func (a *App) startDiagnostic(p *Panel, cmd appconfig.CommandConfig, items []models.Item, gvr schema.GroupVersionResource) tea.Cmd {
	if run := a.diagnostics.runs[cmd.Name]; run != nil {
		return a.askStopDiagnostic(run)
	}
	d := cmd.Diagnostic
	if _, err := diagnostic.LookPath(d.Args); err != nil {
		return a.commandError(cmd, err)
	}
	if _, ok := diagnostic.Lookup(d.Adapter); !ok {
		return a.commandError(cmd, fmt.Errorf("adapter %q is unknown", d.Adapter))
	}
	a.diagnostics.seq++
	run := &diagnosticRun{id: a.diagnostics.seq, cmd: cmd, scopes: diagnosticScopes(items), started: time.Now()}
	run.slot = fmt.Sprintf("diag-%d", run.id)

	supplier := panelCommandExec{app: a, panel: p}
	exec, err := supplier.CommandExec(run.slot, diagnosticNamespace(items))
	if err != nil {
		return a.commandError(cmd, fmt.Errorf("preparing kubeconfig: %w", err))
	}
	ec := exec.Context
	run.release = func() { supplier.ReleaseCommandExec(run.slot) }
	run.context, run.ns = ec.Context, ec.Namespace
	if run.argv, err = RenderDiagnosticArgs(d.Args, items, gvr, ec); err != nil {
		run.release()
		return a.commandError(cmd, err)
	}

	ctx, cancel := context.WithCancel(a.ctx)
	run.cancel = cancel
	if a.diagnostics.runs == nil {
		a.diagnostics.runs = make(map[string]*diagnosticRun)
	}
	a.diagnostics.runs[cmd.Name] = run
	log := ctrllog.FromContext(ctx).WithName("diagnostic")
	log.Info("running diagnostic", "name", cmd.Name, "adapter", d.Adapter, "args", run.argv,
		"context", run.context, "namespace", run.ns, "timeout", d.EffectiveTimeout())
	proc := diagnostic.Process{Argv: run.argv, Env: exec.Env, Timeout: d.EffectiveTimeout()}
	work := func() tea.Msg {
		return diagnosticDoneMsg{run: run, res: diagnostic.Run(ctx, proc)}
	}
	notice := fmt.Sprintf("%s: running %s for %s; pick it again to stop", cmd.Name, run.argv[0], scopeText(run))
	return tea.Batch(a.ShowInfo(notice, 5*time.Second), work)
}

// scopeText names what a run looks at.
func scopeText(run *diagnosticRun) string {
	if len(run.scopes) == 0 {
		if run.ns != "" {
			return "namespace " + run.ns
		}
		return "the whole context"
	}
	parts := make([]string, len(run.scopes))
	for i, s := range run.scopes {
		parts[i] = s.String()
	}
	return strings.Join(parts, ", ")
}

// askStopDiagnostic asks whether to stop run.
func (a *App) askStopDiagnostic(run *diagnosticRun) tea.Cmd {
	c := models.Confirmation{
		Title:   "Stop " + run.cmd.Name + "?",
		Details: diagnosticDetails(run),
		Notes:   []string{fmt.Sprintf("Running for %s. kc interrupts the tool and discards its output.", time.Since(run.started).Round(time.Second))},
		Action:  "Stop",
	}
	inner := NewConfirmModel(c)
	model := &commandConfirmModel{ConfirmModel: inner, onAnswer: func(ok bool) tea.Cmd {
		a.hideModalName(diagnosticStopModal)
		if ok {
			a.stopDiagnostic(run.cmd.Name, run.id)
		}
		return nil
	}}
	modal := NewModal("Diagnostic", model)
	a.modalManager.Register(diagnosticStopModal, modal)
	w, h := inner.size()
	a.configureModalWindow(modal, model, a.activePanel, "", min(w+2, max(30, a.width-6)), h+2)
	a.showModal(diagnosticStopModal)
	return nil
}

// stopDiagnostic cancels the run id of name if it still runs. The result
// arrives as a Canceled diagnosticDoneMsg.
func (a *App) stopDiagnostic(name string, id int) {
	if run := a.diagnostics.runs[name]; run != nil && run.id == id {
		run.cancel()
	}
}

func diagnosticDetails(run *diagnosticRun) [][2]string {
	var d [][2]string
	if run.context != "" {
		d = append(d, [2]string{"Context", run.context})
	}
	d = append(d, [2]string{"Scope", scopeText(run)})
	d = append(d, [2]string{"Command", execctx.ShellJoin(run.argv)})
	return d
}

// handleDiagnosticDone ends a run and shows its result.
func (a *App) handleDiagnosticDone(msg diagnosticDoneMsg) tea.Cmd {
	run := msg.run
	if cur := a.diagnostics.runs[run.cmd.Name]; cur != nil && cur.id == run.id {
		delete(a.diagnostics.runs, run.cmd.Name)
	}
	run.cancel()
	if run.release != nil {
		run.release()
	}
	res := msg.res
	ctrllog.FromContext(a.ctx).WithName("diagnostic").Info("diagnostic ended", "name", run.cmd.Name,
		"outcome", res.Outcome.String(), "exitCode", res.ExitCode, "elapsed", res.Elapsed.Round(time.Millisecond), "error", errText(res.Err))
	switch res.Outcome {
	case diagnostic.Canceled:
		return a.ShowInfo(run.cmd.Name+": stopped", 3*time.Second)
	case diagnostic.Unavailable:
		return a.commandError(run.cmd, res.Err)
	}
	title, body := diagnosticReportText(run, res)
	return a.showDiagnosticReport(title, body)
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// diagnosticReportText is the viewer title and text for a finished,
// failed or timed-out run. The text starts with what ran where, then the
// outcome, the report and the raw output.
func diagnosticReportText(run *diagnosticRun, res diagnostic.Result) (title, text string) {
	var b strings.Builder
	tool := run.argv[0]
	fmt.Fprintf(&b, "%s\n", run.cmd.Name)
	if run.context != "" {
		fmt.Fprintf(&b, "Context: %s\n", run.context)
	}
	fmt.Fprintf(&b, "Scope: %s\n", scopeText(run))
	fmt.Fprintf(&b, "Command: %s\n", execctx.ShellJoin(run.argv))
	fmt.Fprintf(&b, "Started: %s, took %s\n", run.started.Format(time.DateTime), res.Elapsed.Round(time.Second))
	outcome := res.Outcome.String()
	var report *diagnostic.Report
	switch res.Outcome {
	case diagnostic.Finished:
		parse, _ := diagnostic.Lookup(run.cmd.Diagnostic.Adapter)
		rep, err := parse(res.Stdout, run.scopes)
		if err != nil {
			outcome = "output not understood: " + err.Error() + ". kc shows no results for it."
		} else {
			report = &rep
		}
	case diagnostic.TimedOut:
		outcome = fmt.Sprintf("timed out after %s; kc stopped %s and discarded its output", run.cmd.Diagnostic.EffectiveTimeout(), tool)
	case diagnostic.Failed:
		outcome = "failed: " + errText(res.Err)
	}
	fmt.Fprintf(&b, "Outcome: %s\n\n", outcome)
	if report != nil {
		b.WriteString(report.Text())
	}
	if len(res.Stdout) > 0 {
		fmt.Fprintf(&b, "\n--- raw output of %s ---\n", tool)
		b.WriteString(diagnostic.Sanitize(string(res.Stdout)))
		if !strings.HasSuffix(b.String(), "\n") {
			b.WriteString("\n")
		}
	}
	if strings.TrimSpace(res.Stderr) != "" {
		fmt.Fprintf(&b, "\n--- standard error of %s (the end) ---\n", tool)
		b.WriteString(diagnostic.Sanitize(res.Stderr))
		if !strings.HasSuffix(res.Stderr, "\n") {
			b.WriteString("\n")
		}
	}
	title = fmt.Sprintf("%s: %s", run.cmd.Name, res.Outcome)
	if report == nil && res.Outcome == diagnostic.Finished {
		title = run.cmd.Name + ": output not understood"
	}
	return diagnostic.Sanitize(title), b.String()
}

// showDiagnosticReport opens the report in a full-screen viewer.
func (a *App) showDiagnosticReport(title, body string) tea.Cmd {
	view := NewTextViewer(title, body, "text", "text/plain", "diagnostic.txt", a.viewerTheme(), nil, nil, func() tea.Cmd {
		a.hideModalName(diagnosticReportModal)
		return nil
	})
	view.SetWrapMode(false)
	modal := NewModal(title, view)
	modal.SetMode(ModalModeFullscreen)
	modal.SetDimensions(a.width, a.height)
	modal.SetCloseOnSingleEsc(false)
	modal.SetCloseOnEscTimeout(true)
	a.modalManager.Register(diagnosticReportModal, modal)
	a.showModal(diagnosticReportModal)
	return nil
}
