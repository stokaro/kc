package models

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"charm.land/lipgloss/v2"

	"github.com/stokaro/kc/internal/helm"
	table "github.com/stokaro/kc/internal/table"
)

// helmEntryID is the row ID of the /helm entry in resource listings.
const helmEntryID = "helm"

// helmTimeLayout formats release times in rows.
const helmTimeLayout = "2006-01-02 15:04:05"

var helmNoticeStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF5C5C"))

// newHelmEntryItem is the /helm row of a resource listing. It lists the
// Helm releases of namespace (AllNamespaces for all). Nothing is read until
// it is entered.
func newHelmEntryItem(deps Deps, namespace string, parent []string) *ChildResourceItem {
	path := append(append([]string{}, parent...), "helm")
	item := NewChildResourceItem(helmEntryID, []string{"/helm", helm.SecretType, ""}, path, func() (Folder, error) {
		return NewHelmReleasesFolder(deps, namespace, path), nil
	})
	item.SetDetails("Helm releases (stored as Secrets of type " + helm.SecretType + ")")
	return item
}

// newHelmSource is the row source of a Helm folder: rows load off the UI
// thread and reload when the release Secrets change.
func newHelmSource(base *BaseFolder, reader *helmReader, namespace, release string, populate func(context.Context) ([]table.Row, error)) *liveObjectRowSource {
	src := newLiveObjectRowSourceWithHooks(populate, base.markDirtyFromSource, reader.watchFactory(namespace, release))
	src.setTarget(liveSourceTarget{gvr: secretsGVR, namespace: namespace})
	src.useBackground(base.Deps)
	src.watchTTL = watchDuration(base.Deps)
	src.reportErr = listErrorReporter(base.Deps, secretsGVR, namespace)
	return src
}

// helmNotice is a non-enterable row that explains a problem; F3 shows the
// full text.
func helmNotice(id, short, detail string, path []string, columns int) *SimpleItem {
	style := helmNoticeStyle
	return helmNoticeStyled(id, short, detail, path, columns, &style)
}

func helmNoticeStyled(id, short, detail string, path []string, columns int, style *lipgloss.Style) *SimpleItem {
	cells := make([]string, columns)
	cells[0] = short
	item := NewSimpleItem(id, cells, append(append([]string{}, path...), id), style)
	item.SetDetails(detail)
	item.WithViewContent(func() (string, string, string, string, string, error) {
		return "Helm", detail + "\n", "", "", "helm-notice.txt", nil
	})
	return item
}

// helmAccessNotice turns a denied access probe into an actionable row.
func helmAccessNotice(deps Deps, ae *helmAccessError, namespace string, path []string, columns int) *SimpleItem {
	var detail strings.Builder
	detail.WriteString(ae.Error())
	detail.WriteString(".\n\n")
	switch ae.access {
	case helmAccessNamespaceOnly:
		detail.WriteString("Ask for get, list and watch on secrets in all namespaces, or use helm directly:\n\n  ")
	default:
		detail.WriteString("kc reads Helm releases from Secrets of type " + helm.SecretType + " (label owner=helm).\n")
		detail.WriteString("Ask for get, list and watch on secrets in all namespaces, or use helm directly:\n\n  ")
	}
	detail.WriteString(helmListCommand(deps, namespace))
	detail.WriteString("\n")
	if ae.cause != nil {
		detail.WriteString("\nAPI server: " + ae.cause.Error() + "\n")
	}
	return helmNotice("__helm_denied__", "! cannot read Helm releases: Secrets forbidden", detail.String(), path, columns)
}

func formatHelmTime(t helm.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Local().Format(helmTimeLayout)
}

func scopeText(namespace string) string {
	if namespace == AllNamespaces {
		return "all namespaces"
	}
	return namespace
}

// HelmReleasesFolder lists the Helm releases of a namespace scope, newest
// revision of each, from the Secrets in the shared cache.
type HelmReleasesFolder struct {
	*BaseFolder
	namespace string
	reader    *helmReader
	src       *liveObjectRowSource

	reportOnce sync.Once
}

var helmReleaseColumns = []table.Column{
	{Title: " Name"}, {Title: "Namespace"}, {Title: "Revision"}, {Title: "Status"},
	{Title: "Chart"}, {Title: "App version"}, {Title: "Updated"},
}

// NewHelmReleasesFolder lists the releases in namespace (AllNamespaces for
// all namespaces).
func NewHelmReleasesFolder(deps Deps, namespace string, path []string) *HelmReleasesFolder {
	return newHelmReleasesFolder(deps, newHelmReader(deps), namespace, path)
}

func newHelmReleasesFolder(deps Deps, reader *helmReader, namespace string, path []string) *HelmReleasesFolder {
	base := NewBaseFolder(deps, helmReleaseColumns, path)
	f := &HelmReleasesFolder{BaseFolder: base, namespace: namespace, reader: reader}
	f.src = newHelmSource(base, reader, namespace, "", f.populate)
	base.SetRowSource(f.src)
	return f
}

// Namespace returns the folder's namespace scope.
func (f *HelmReleasesFolder) Namespace() string { return f.namespace }

// Close stops the watch.
func (f *HelmReleasesFolder) Close() { f.src.Close() }

func (f *HelmReleasesFolder) populate(ctx context.Context) ([]table.Row, error) {
	recs, err := f.reader.records(ctx, f.namespace, "")
	if ae, ok := isHelmAccessError(err); ok {
		f.reportOnce.Do(func() {
			if f.Deps.ReportError != nil {
				f.Deps.ReportError(fmt.Errorf("cannot read Helm releases: %s", ae.Error()))
			}
		})
		return []table.Row{helmAccessNotice(f.Deps, ae, f.namespace, f.Path(), len(helmReleaseColumns))}, nil
	}
	if err != nil {
		return nil, err
	}
	return helmReleaseRows(f.Deps, f.reader, helm.Group(recs), f.namespace, f.Path()), nil
}

// helmReleaseRows builds one row per release from its newest revision.
func helmReleaseRows(deps Deps, reader *helmReader, histories []helm.History, namespace string, path []string) []table.Row {
	if len(histories) == 0 {
		detail := fmt.Sprintf("No Helm release is stored as a Secret in %s.\n\nkc reads Helm's default storage (Secrets of type %s). Releases stored with HELM_DRIVER=configmap or sql are not shown.", scopeText(namespace), helm.SecretType)
		notice := helmNoticeStyled("__helm_none__", "no Helm releases in "+scopeText(namespace), detail, path, len(helmReleaseColumns), DimStyle())
		return []table.Row{notice}
	}
	rows := make([]table.Row, 0, len(histories))
	for _, h := range histories {
		rows = append(rows, newHelmReleaseItem(deps, reader, h, path))
	}
	return rows
}

// HelmReleaseItem is one release in the release list.
type HelmReleaseItem struct {
	*RowItem
	deps      Deps
	reader    *helmReader
	namespace string
	release   string
	summary   string
}

func newHelmReleaseItem(deps Deps, reader *helmReader, h helm.History, parent []string) *HelmReleaseItem {
	latest := h.Latest()
	rel, err := reader.decode(latest)
	status := latest.Status
	chart, appVersion, updated := "?", "", ""
	if err == nil {
		if rel.Info.Status != "" {
			status = rel.Info.Status
		}
		chart = rel.ChartName()
		appVersion = rel.AppVersion()
		updated = formatHelmTime(rel.Info.LastDeployed)
	}
	cells := []string{"/" + h.Release, h.Namespace, strconv.Itoa(latest.Revision), status, chart, appVersion, updated}
	path := append(append([]string{}, parent...), h.Release)
	item := &HelmReleaseItem{
		RowItem:   NewRowItem(h.Namespace+"/"+h.Release, cells, path, WhiteStyle()),
		deps:      deps,
		reader:    reader,
		namespace: h.Namespace,
		release:   h.Release,
	}
	if err != nil {
		item.SetDetails(err.Error())
		item.summary = fmt.Sprintf("NAME: %s\nNAMESPACE: %s\nREVISION: %d\nSTATUS: %s\n\nThe stored record cannot be decoded: %v\n", h.Release, h.Namespace, latest.Revision, status, err)
	} else {
		item.SetDetails(fmt.Sprintf("%s in %s, revision %d, %s", h.Release, h.Namespace, latest.Revision, status))
		item.summary = helmStatusText(rel)
	}
	item.SetViewCommand(helmCommand(deps, "status", h.Release, "-n", h.Namespace))
	return item
}

// Enter opens the newest revision of the release.
func (i *HelmReleaseItem) Enter() (Folder, error) {
	return newHelmReleaseFolder(i.deps, i.reader, i.namespace, i.release, 0, i.Path()), nil
}

// ViewContent shows the stored status of the newest revision, like
// "helm status".
func (i *HelmReleaseItem) ViewContent() (string, string, string, string, string, error) {
	return i.release + " (stored release)", i.summary, "", "", i.release + "-status.txt", nil
}

// helmStatusText is what "helm status" prints for a stored release.
func helmStatusText(rel *helm.Release) string {
	var b strings.Builder
	fmt.Fprintf(&b, "NAME: %s\n", rel.Name)
	fmt.Fprintf(&b, "LAST DEPLOYED: %s\n", formatHelmTime(rel.Info.LastDeployed))
	fmt.Fprintf(&b, "NAMESPACE: %s\n", rel.Namespace)
	fmt.Fprintf(&b, "STATUS: %s\n", rel.Info.Status)
	fmt.Fprintf(&b, "REVISION: %d\n", rel.Version)
	if c := rel.ChartName(); c != "" {
		fmt.Fprintf(&b, "CHART: %s\n", c)
	}
	if v := rel.AppVersion(); v != "" {
		fmt.Fprintf(&b, "APP VERSION: %s\n", v)
	}
	if rel.Info.Description != "" {
		fmt.Fprintf(&b, "DESCRIPTION: %s\n", rel.Info.Description)
	}
	if notes := strings.TrimSpace(rel.Info.Notes); notes != "" {
		fmt.Fprintf(&b, "NOTES:\n%s\n", notes)
	}
	return b.String()
}
