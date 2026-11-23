package ui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	textinput "charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/stokaro/kc/internal/metricsources"
	"github.com/stokaro/kc/internal/promhistory"
	"github.com/stokaro/kc/internal/ui/dialog"
	"github.com/stokaro/kc/internal/ui/menubar"
	"github.com/stokaro/kc/pkg/appconfig"
)

// metricSourcesKey opens the metric sources dialog after C-x
// (docs/adr/0257-metric-sources.md).
const metricSourcesKey = "m"

// metricSourcesModalName registers the dialog with the modal manager.
const metricSourcesModalName = "metric_sources"

// metricSourcesRows is how many report lines the dialog shows at most; a
// short screen shows fewer, and the last line then counts the rest.
const metricSourcesRows = 11

const (
	sourcesTestID = "test"
	sourcesFindID = "find"
	sourcesUseID  = "use"
	sourcesSaveID = "save"
	sourcesDoneID = "close"
)

// metricSourcesReportMsg carries a finished report to the dialog that
// asked for it; seq drops a report of an earlier request.
type metricSourcesReportMsg struct {
	seq    int
	checks []metricsources.Check
	err    error
}

// metricSourcesFoundMsg carries the Services that look like a backend.
type metricSourcesFoundMsg struct {
	seq        int
	candidates []metricsources.Candidate
	err        error
}

// metricSourcesDialog shows which sources can answer kc's usage and
// history questions in the current context and scope, and edits the
// Prometheus backend of the context. Nothing is checked until the user
// presses Test (or the dialog opens), and Services are listed only on
// Find.
type metricSourcesDialog struct {
	app       *App
	context   string
	namespace string

	form    dialog.Form
	service textinput.Model
	prefix  textinput.Model
	labels  textinput.Model
	https   bool

	seq        int
	lines      []string
	errLines   map[int]bool
	note       string
	isErr      bool
	candidates []metricsources.Candidate
	next       int
	rows       int
	width      int
	rules      []int
}

func newMetricSourcesDialog(a *App, contextName, namespace string, b appconfig.MetricsBackendConfig, configured bool) *metricSourcesDialog {
	skin := dialog.Normal()
	d := &metricSourcesDialog{
		app: a, context: contextName, namespace: namespace,
		form:    dialog.Form{Skin: skin},
		service: dialog.NewInput(skin, ""),
		prefix:  dialog.NewInput(skin, ""),
		labels:  dialog.NewInput(skin, ""),
		rows:    metricSourcesRows,
	}
	d.service.Placeholder = "monitoring/prometheus-k8s:9090"
	d.prefix.Placeholder = "/prometheus for Mimir; empty for Prometheus"
	d.labels.Placeholder = "cluster=prod-eu"
	if configured {
		d.setBackend(b)
	}
	d.syncItems()
	return d
}

func (d *metricSourcesDialog) setBackend(b appconfig.MetricsBackendConfig) {
	svc := ""
	if b.Namespace != "" || b.Service != "" {
		svc = b.Namespace + "/" + b.Service
		if b.Port != "" {
			svc += ":" + b.Port
		}
	}
	d.service.SetValue(svc)
	d.prefix.SetValue(b.PathPrefix)
	d.labels.SetValue(formatLabels(b.Labels))
	d.https = b.Scheme == "https"
}

func (d *metricSourcesDialog) syncItems() {
	scope := "all namespaces"
	if d.namespace != "" {
		scope = "namespace " + d.namespace
	}
	items := []dialog.Item{
		dialog.Text{Text: "Context " + d.context + ", " + scope, Bold: true},
		dialog.Text{Text: "Prometheus Service (namespace/name[:port]), reached through the API server:"},
		dialog.Input{Model: &d.service},
		dialog.Text{Text: "Path prefix:"},
		dialog.Input{Model: &d.prefix},
		dialog.Text{Text: "Labels on every series (name=value, ...), never guessed:"},
		dialog.Input{Model: &d.labels},
		dialog.Check{ID: "https", Label: "Proxy to the Service over https", On: &d.https},
		dialog.Text{Text: d.note, Error: d.isErr},
		dialog.Rule{},
	}
	for i := range d.rows {
		line := ""
		if i < len(d.lines) {
			line = d.lines[i]
		}
		if i == d.rows-1 && len(d.lines) > d.rows {
			line = fmt.Sprintf("... and %d more", len(d.lines)-d.rows+1)
		}
		items = append(items, dialog.Text{Text: line, Error: d.errLines[i]})
	}
	items = append(items,
		dialog.Rule{},
		dialog.Buttons{
			{ID: sourcesTestID, Label: "&Test", Default: true},
			{ID: sourcesFindID, Label: "&Find"},
			{ID: sourcesUseID, Label: "&Use"},
			{ID: sourcesSaveID, Label: "&Save"},
			{ID: sourcesDoneID, Label: "&Close"},
		},
	)
	d.form.Items = items
}

func (d *metricSourcesDialog) Init() tea.Cmd { return d.form.FocusFirst() }

func (d *metricSourcesDialog) SetDimensions(w, _ int) { d.width = w }

// PreferredSize implements ModalSizer.
func (d *metricSourcesDialog) PreferredSize(maxW, maxH int) (width, height int) {
	d.rows = metricSourcesRows
	d.syncItems()
	if fixed := len(d.form.Items) - d.rows; maxH > 0 {
		d.rows = max(2, min(metricSourcesRows, maxH-fixed))
	}
	d.syncItems()
	width = 96
	if maxW > 0 {
		width = min(width, maxW)
	}
	return width, d.form.Height()
}

// FooterHints implements ModalFooterHints.
func (d *metricSourcesDialog) FooterHints() []FooterHint {
	return []FooterHint{
		{Key: "Enter", Label: "Test", Enabled: true},
		{Key: "C-s", Label: "Save", Enabled: true},
		{Key: "Esc", Label: "Close", Enabled: true},
	}
}

// RuleRows implements ModalRules.
func (d *metricSourcesDialog) RuleRows() []int { return d.rules }

func (d *metricSourcesDialog) View() tea.View {
	d.syncItems()
	content, cursor, rules := d.form.View(max(d.width, 20))
	d.rules = rules
	return viewWithCursor(content, cursor)
}

func (d *metricSourcesDialog) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case metricSourcesReportMsg:
		d.showReport(m)
		return d, nil
	case metricSourcesFoundMsg:
		d.showFound(m)
		return d, nil
	case tea.KeyPressMsg:
		if m.String() == "ctrl+s" {
			return d, d.save()
		}
	}
	d.syncItems()
	ev, cmd := d.form.Update(msg)
	if ev.Edited || ev.Toggled != "" {
		d.note, d.isErr = "", false
	}
	switch ev.Pressed {
	case sourcesTestID:
		return d, d.test()
	case sourcesFindID:
		return d, d.find()
	case sourcesUseID:
		return d, d.use()
	case sourcesSaveID:
		return d, d.save()
	case sourcesDoneID:
		d.app.hideModalName(metricSourcesModalName)
		return d, nil
	}
	return d, cmd
}

// backend reads the fields. An empty Service field means no backend.
func (d *metricSourcesDialog) backend() (appconfig.MetricsBackendConfig, bool, error) {
	b := appconfig.MetricsBackendConfig{Contexts: []string{d.context}, PathPrefix: strings.TrimSpace(d.prefix.Value())}
	svc := strings.TrimSpace(d.service.Value())
	if svc == "" {
		return b, false, nil
	}
	ns, name, ok := strings.Cut(svc, "/")
	if !ok || ns == "" || name == "" {
		return b, false, errors.New("service: namespace/name or namespace/name:port")
	}
	b.Namespace = ns
	b.Service, b.Port, _ = strings.Cut(name, ":")
	if b.Service == "" {
		return b, false, errors.New("service: the name is missing")
	}
	if d.https {
		b.Scheme = "https"
	}
	labels, err := parseLabels(d.labels.Value())
	if err != nil {
		return b, false, err
	}
	b.Labels = labels
	// NewServiceProxyClient validates the label names and the scheme.
	if _, err := promhistory.NewServiceProxyClient(&rest.Config{Host: "https://check.invalid"}, b); err != nil {
		return b, false, err
	}
	return b, true, nil
}

// test runs the report off the UI goroutine with the fields as they are.
func (d *metricSourcesDialog) test() tea.Cmd {
	b, ok, err := d.backend()
	if err != nil {
		d.note, d.isErr = err.Error(), true
		return nil
	}
	a := d.app
	if a.cl == nil {
		d.note, d.isErr = "not connected to a cluster", true
		return nil
	}
	d.seq++
	seq, cl, root, timeout := d.seq, a.cl, a.ctx, a.cfg.MetricsHistory.EffectiveTimeout()
	ctxName, ns := d.context, d.namespace
	d.note, d.isErr = "Testing...", false
	return func() tea.Msg {
		// Each check is one request; the whole report gets twice the
		// timeout of one history read.
		ctx, cancel := context.WithTimeout(root, 2*timeout)
		defer cancel()
		cfg := cl.GetConfig()
		cs, err := kubernetes.NewForConfig(rest.CopyConfig(cfg))
		if err != nil {
			return metricSourcesReportMsg{seq: seq, err: err}
		}
		req := metricsources.Request{
			Context: ctxName, Namespace: ns,
			Kube: metricsources.Kube{Discovery: cl.DiscoveryClient(), Reviews: cs.AuthorizationV1()},
		}
		if ok {
			c, err := promhistory.NewServiceProxyClient(cfg, b)
			if err != nil {
				return metricSourcesReportMsg{seq: seq, err: err}
			}
			req.Backend, req.Source = c, c.Source
		}
		return metricSourcesReportMsg{seq: seq, checks: metricsources.Run(ctx, req)}
	}
}

func (d *metricSourcesDialog) showReport(m metricSourcesReportMsg) {
	if m.seq != d.seq {
		return
	}
	if m.err != nil {
		d.note, d.isErr = m.err.Error(), true
		return
	}
	d.lines = metricsources.Text(m.checks)
	d.errLines = make(map[int]bool)
	bad := 0
	for i, c := range m.checks {
		if !c.State.Usable() {
			d.errLines[i] = true
			bad++
		}
	}
	d.note, d.isErr = fmt.Sprintf("Tested at %s: %d of %d checks ok", time.Now().Format("15:04:05"), len(m.checks)-bad, len(m.checks)), false
}

// find lists Services once and fills in the first candidate; pressing
// Find again fills in the next one.
func (d *metricSourcesDialog) find() tea.Cmd {
	if len(d.candidates) > 0 {
		d.fillCandidate()
		return nil
	}
	a := d.app
	if a.cl == nil {
		d.note, d.isErr = "not connected to a cluster", true
		return nil
	}
	d.seq++
	seq, cl, root, timeout := d.seq, a.cl, a.ctx, a.cfg.MetricsHistory.EffectiveTimeout()
	d.note, d.isErr = "Listing Services...", false
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(root, timeout)
		defer cancel()
		cs, err := kubernetes.NewForConfig(rest.CopyConfig(cl.GetConfig()))
		if err != nil {
			return metricSourcesFoundMsg{seq: seq, err: err}
		}
		found, err := metricsources.FindCandidates(ctx, cs.CoreV1())
		return metricSourcesFoundMsg{seq: seq, candidates: found, err: err}
	}
}

func (d *metricSourcesDialog) showFound(m metricSourcesFoundMsg) {
	if m.seq != d.seq {
		return
	}
	switch {
	case m.err != nil:
		d.note, d.isErr = m.err.Error(), true
	case len(m.candidates) == 0:
		d.note, d.isErr = "No Service looks like Prometheus, Thanos, Mimir or VictoriaMetrics; enter it", true
	default:
		d.candidates, d.next = m.candidates, 0
		d.fillCandidate()
	}
}

func (d *metricSourcesDialog) fillCandidate() {
	c := d.candidates[d.next%len(d.candidates)]
	d.next++
	d.service.SetValue(c.String())
	d.service.CursorEnd()
	d.note, d.isErr = fmt.Sprintf("Candidate %d of %d; Test checks it, Find shows the next", (d.next-1)%len(d.candidates)+1, len(d.candidates)), false
}

// use applies the backend to this session.
func (d *metricSourcesDialog) use() tea.Cmd {
	b, ok, err := d.backend()
	if err != nil {
		d.note, d.isErr = err.Error(), true
		return nil
	}
	setContextBackend(&d.app.cfg.MetricsHistory, d.context, b, ok)
	d.app.invalidateView("metric sources")
	d.app.hideModalName(metricSourcesModalName)
	return d.app.sessionOnly()
}

// save applies the backend to this session and writes it to the
// configuration file.
func (d *metricSourcesDialog) save() tea.Cmd {
	b, ok, err := d.backend()
	if err != nil {
		d.note, d.isErr = err.Error(), true
		return nil
	}
	ctxName := d.context
	setContextBackend(&d.app.cfg.MetricsHistory, ctxName, b, ok)
	d.app.invalidateView("metric sources")
	d.app.hideModalName(metricSourcesModalName)
	return d.app.saveDefaults(func(cfg *appconfig.Config) {
		setContextBackend(&cfg.MetricsHistory, ctxName, b, ok)
	})
}

// setContextBackend makes b the backend of exactly contextName: it
// replaces an entry that names only that context, else it goes first, so
// it wins over a glob entry. ok false removes that entry, which leaves a
// glob entry in charge.
func setContextBackend(mh *appconfig.MetricsHistoryConfig, contextName string, b appconfig.MetricsBackendConfig, ok bool) {
	idx := slices.IndexFunc(mh.Backends, func(e appconfig.MetricsBackendConfig) bool {
		return len(e.Contexts) == 1 && e.Contexts[0] == contextName
	})
	switch {
	case !ok && idx >= 0:
		mh.Backends = slices.Delete(slices.Clone(mh.Backends), idx, idx+1)
	case !ok:
	case idx >= 0:
		mh.Backends = slices.Clone(mh.Backends)
		mh.Backends[idx] = b
	default:
		mh.Backends = append([]appconfig.MetricsBackendConfig{b}, mh.Backends...)
	}
}

// parseLabels reads "name=value, name=value".
func parseLabels(s string) (map[string]string, error) {
	out := make(map[string]string)
	for part := range strings.SplitSeq(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, ok := strings.Cut(part, "=")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if !ok || k == "" {
			return nil, fmt.Errorf("labels: %q is not name=value", part)
		}
		out[k] = strings.Trim(v, `"`)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func formatLabels(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+m[k])
	}
	return strings.Join(parts, ", ")
}

// showMetricSources opens the dialog for the current context and the
// namespace scope of the active panel, and runs the report at once: the
// checks are a handful of bounded requests the user asked for by opening
// it.
func (a *App) showMetricSources() tea.Cmd {
	if a.cl == nil {
		if a.toastLogger != nil {
			return a.toastLogger.Errorf("Metric sources: not connected to a cluster")
		}
		return nil
	}
	key := a.currentClusterKey()
	ns := a.overviewNamespace(a.activePanel)
	b, configured := a.cfg.MetricsHistory.BackendFor(key.ContextName)
	d := newMetricSourcesDialog(a, key.ContextName, ns, b, configured)
	modal := NewModal("Metric sources", d)
	modal.SetCloseOnSingleEsc(true)
	a.modalManager.Register(metricSourcesModalName, modal)
	a.configureModalWindow(modal, d, -1, "", 96, 26)
	a.showModal(metricSourcesModalName)
	return tea.Batch(d.Init(), d.test())
}

// metricSourcesMenuItem is the Command menu entry of the dialog.
func (a *App) metricSourcesMenuItem() menubar.Item {
	return menubar.Item{Label: "&Metric sources...", Shortcut: "C-x m", Run: a.showMetricSources}
}

// routeMetricSources hands an async result to the open dialog.
func (a *App) routeMetricSources(msg tea.Msg) (tea.Cmd, bool) {
	switch msg.(type) {
	case metricSourcesReportMsg, metricSourcesFoundMsg:
	default:
		return nil, false
	}
	m := a.modalManager.modals[metricSourcesModalName]
	if m == nil {
		return nil, true
	}
	if d, ok := m.content.(*metricSourcesDialog); ok {
		_, cmd := d.Update(msg)
		a.invalidateView("metric sources report")
		return cmd, true
	}
	return nil, true
}
