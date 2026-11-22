package models

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/cli-runtime/pkg/printers"
	"k8s.io/client-go/util/jsonpath"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	table "github.com/stokaro/kc/internal/table"
	"github.com/stokaro/kc/pkg/appconfig"
)

// customColumns is the column-merge step for one object list load: it hides
// server Table columns and appends columns computed with JSONPath from the
// full objects in the shared cache (the Table rows carry metadata only).
//
// A nil *customColumns changes nothing, so callers need no checks. It is
// built per load and used by one goroutine (jsonpath.JSONPath is not safe
// for concurrent use).
type customColumns struct {
	gvr     schema.GroupVersionResource
	hideAll bool
	hide    map[string]struct{} // lower-case server column names
	cols    []compiledColumn
	objects map[string]*unstructured.Unstructured // by namespace/name
	report  func(title string, err error)         // once per GVR and column
}

type compiledColumn struct {
	title  string
	expr   string             // as configured, for kubectl -o custom-columns
	parser *jsonpath.JSONPath // nil when the expression is invalid
}

// customColumnsLookupTimeout bounds waiting for the object informer. The
// list's own informer is the same one, so it is usually synced; if not, the
// columns stay empty and the sync event reloads the list.
const customColumnsLookupTimeout = 5 * time.Second

// reportedColumnErrors keeps each columns config or evaluation error to one
// report per process, instead of one per row and load.
var reportedColumnErrors sync.Map

func reportColumnErrorOnce(deps Deps, key string, err error) {
	if _, seen := reportedColumnErrors.LoadOrStore(key, struct{}{}); seen {
		return
	}
	ctrllog.FromContext(deps.Ctx).WithName("customColumns").Info("columns config error", "error", err.Error())
	if deps.ReportError != nil {
		deps.ReportError(err)
	}
}

// reportColumnsConfig reports every invalid entry of the columns config in
// one message, once.
func reportColumnsConfig(deps Deps, cfg map[string][]appconfig.CustomColumn) {
	errs := appconfig.ValidateColumns(cfg)
	if len(errs) == 0 {
		return
	}
	msgs := make([]string, len(errs))
	for i, e := range errs {
		msgs[i] = e.Error()
	}
	msg := strings.Join(msgs, "; ")
	reportColumnErrorOnce(deps, "config\x00"+msg, errors.New("config: "+msg))
}

// planCustomColumns compiles the columns config for gvr and mode. It returns
// nil when nothing is configured for gvr. Invalid entries are reported once
// and leave their column empty.
func planCustomColumns(deps Deps, gvr schema.GroupVersionResource, mode string) *customColumns {
	if deps.AppConfig == nil || len(deps.AppConfig.Columns) == 0 {
		return nil
	}
	cfg := deps.AppConfig.Columns
	reportColumnsConfig(deps, cfg)
	entries := cfg[appconfig.ColumnsKey(gvr.GroupVersion().String(), gvr.Resource)]
	if len(entries) == 0 {
		return nil
	}
	cc := &customColumns{
		gvr:  gvr,
		hide: map[string]struct{}{},
		report: func(title string, err error) {
			reportColumnErrorOnce(deps, "eval\x00"+gvr.String()+"\x00"+title, fmt.Errorf("column %s of %s: %w", title, gvr.Resource, err))
		},
	}
	for _, e := range entries {
		name := strings.TrimSpace(e.Name)
		if e.Validate() != nil {
			// Reported by reportColumnsConfig. Keep a bad jsonPath column
			// visible and empty, so the user sees where it would be.
			if name == "" || e.Hide || strings.TrimSpace(e.JSONPath) == "" {
				continue
			}
		}
		if e.Hide {
			if name == appconfig.HideAllServerColumns {
				cc.hideAll = true
			} else {
				cc.hide[strings.ToLower(name)] = struct{}{}
			}
			continue
		}
		if e.Wide && mode != appconfig.ColumnsModeWide {
			continue
		}
		expr := strings.TrimSpace(e.JSONPath)
		p, _ := appconfig.ParseCustomColumn(expr)
		cc.cols = append(cc.cols, compiledColumn{title: name, expr: expr, parser: p})
	}
	return cc
}

// loadObjects reads the full objects for the added columns from the shared
// informer cache. Without added columns it reads nothing.
func (c *customColumns) loadObjects(ctx context.Context, deps Deps, namespace string) {
	if c == nil || len(c.cols) == 0 || deps.Cl == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, customColumnsLookupTimeout)
	defer cancel()
	list, err := deps.Cl.ListByGVR(ctx, c.gvr, namespace)
	if err != nil {
		ctrllog.FromContext(ctx).WithName("customColumns").V(1).Info("objects for custom columns not available", "gvr", c.gvr.String(), "namespace", namespace, "error", err.Error())
		return
	}
	c.setObjects(list.Items)
}

func (c *customColumns) setObjects(items []unstructured.Unstructured) {
	if c == nil || len(c.cols) == 0 {
		return
	}
	c.objects = make(map[string]*unstructured.Unstructured, len(items))
	for i := range items {
		c.objects[items[i].GetNamespace()+"/"+items[i].GetName()] = &items[i]
	}
}

// serverColumns drops the hidden columns from the visible server column
// indexes. The first column (Name) always stays.
func (c *customColumns) serverColumns(defs []metav1.TableColumnDefinition, vis []int) []int {
	if c == nil || (!c.hideAll && len(c.hide) == 0) {
		return vis
	}
	out := make([]int, 0, len(vis))
	for _, idx := range vis {
		if idx == 0 {
			out = append(out, idx)
			continue
		}
		if c.hideAll {
			continue
		}
		if idx < len(defs) {
			if _, hidden := c.hide[strings.ToLower(defs[idx].Name)]; hidden {
				continue
			}
		}
		out = append(out, idx)
	}
	return out
}

// tableColumns returns the added column headers.
func (c *customColumns) tableColumns() []table.Column {
	if c == nil {
		return nil
	}
	out := make([]table.Column, len(c.cols))
	for i, col := range c.cols {
		out[i] = table.Column{Title: col.title}
	}
	return out
}

// cells returns the added cells for one object. A missing object gives
// empty cells.
func (c *customColumns) cells(namespace, name string) []string {
	if c == nil || len(c.cols) == 0 {
		return nil
	}
	return c.cellsFor(c.objects[namespace+"/"+name])
}

// cellsFor evaluates the added columns on obj the way kubectl custom-columns
// does: values of all matches joined by ",", "<none>" when nothing matches.
func (c *customColumns) cellsFor(obj *unstructured.Unstructured) []string {
	if c == nil || len(c.cols) == 0 {
		return nil
	}
	out := make([]string, len(c.cols))
	if obj == nil {
		return out
	}
	for i, col := range c.cols {
		if col.parser == nil {
			continue
		}
		values, err := col.parser.FindResults(obj.UnstructuredContent())
		if err != nil {
			if c.report != nil {
				c.report(col.title, err)
			}
			continue
		}
		if len(values) == 0 || len(values[0]) == 0 {
			out[i] = "<none>"
			continue
		}
		var parts []string
		for _, set := range values {
			for _, v := range set {
				parts = append(parts, printers.EscapeTerminal(fmt.Sprint(v.Interface())))
			}
		}
		out[i] = strings.Join(parts, ",")
	}
	return out
}

// kubectlArgs returns "-o custom-columns=..." for the added columns and a
// note on what the command does not show. kubectl splits the spec at "," and
// the first ":", so a column using either cannot be written and the note
// says so instead.
func (c *customColumns) kubectlArgs(allNamespaces bool) ([]string, string) {
	if c == nil || len(c.cols) == 0 {
		return nil, ""
	}
	specs := []string{"NAME:.metadata.name"}
	if allNamespaces {
		specs = append(specs, "NAMESPACE:.metadata.namespace")
	}
	for _, col := range c.cols {
		if strings.ContainsAny(col.title, ":,") || strings.Contains(col.expr, ",") {
			return nil, "kc adds custom columns"
		}
		specs = append(specs, col.title+":"+col.expr)
	}
	args := []string{"-o", "custom-columns=" + strings.Join(specs, ",")}
	if c.hideAll {
		return args, ""
	}
	return args, "kc also shows the server columns"
}
