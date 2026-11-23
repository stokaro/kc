package models_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stokaro/kc/internal/models"
)

func TestParseRowFilterTerms(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		wantErr  string
		wantText string
		wantSel  string
	}{
		{name: "name and selector", text: "-l app=web  api", wantText: "-l app=web api", wantSel: "app=web"},
		{name: "selector set form, then a namespace", text: "-l env in (a, b) -n prod", wantText: "-l env in (a, b) -n prod", wantSel: "env in (a,b)"},
		{name: "two selectors combine", text: "-l app=web -l tier!=db", wantText: "-l app=web -l tier!=db", wantSel: "app=web,tier!=db"},
		{name: "columns", text: "CPU>500m mem>=1Gi restarts>3", wantText: "CPU>500m mem>=1Gi restarts>3"},
		{name: "value list", text: "status=Running,Pending", wantText: "status=Running,Pending"},
		{name: "regex with a group keeps its space", text: "^(web| api)", wantText: "^(web| api)"},
		{name: "empty value", text: "status=", wantErr: "value is empty"},
		{name: "empty list item", text: "status=a,,b", wantErr: "empty"},
		{name: "ordering takes one value", text: "cpu>1,2", wantErr: "one value"},
		{name: "namespace list missing", text: "web -n", wantErr: "namespace list"},
		{name: "selector missing", text: "web -l", wantErr: "label selector is empty"},
		{name: "bad regex among terms", text: "cpu>1 web(", wantErr: "regular expression"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f, err := models.ParseRowFilter(tc.text)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("ParseRowFilter(%q) error = %v, want one with %q", tc.text, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseRowFilter(%q): %v", tc.text, err)
			}
			if got := f.Text(); got != tc.wantText {
				t.Errorf("Text() = %q, want %q", got, tc.wantText)
			}
			if got := f.Selector(); got != tc.wantSel {
				t.Errorf("Selector() = %q, want %q", got, tc.wantSel)
			}
		})
	}
}

func TestRowFilterTermsCompose(t *testing.T) {
	hourAgo := time.Now().Add(-time.Hour)
	apiProd := models.RowFacts{Name: "api-1", ID: "prod/api-1", Namespace: "prod", Labels: map[string]string{"app": "api"}, Health: models.RowHealthNotReady, Created: hourAgo}
	apiDev := models.RowFacts{Name: "api-1", ID: "dev/api-1", Namespace: "dev", Labels: map[string]string{"app": "api"}, Health: models.RowHealthNotReady, Created: hourAgo}
	webProd := models.RowFacts{Name: "web-1", ID: "prod/web-1", Namespace: "prod", Labels: map[string]string{"app": "web"}, Health: models.RowHealthOK}
	node := models.RowFacts{Name: "n1", ID: "n1"}

	tests := []struct {
		name   string
		text   string
		faults bool
		row    models.RowFacts
		want   bool
	}{
		{name: "all categories match", text: "api -l app=api -n prod,stage", faults: true, row: apiProd, want: true},
		{name: "unselected namespace", text: "api -l app=api -n prod,stage", faults: true, row: apiDev, want: false},
		{name: "faults is one more AND", text: "-n prod", faults: true, row: webProd, want: false},
		{name: "namespace exclusion", text: "namespace!=kube-system,dev", row: apiDev, want: false},
		{name: "namespace exclusion keeps others", text: "namespace!=kube-system,dev", row: apiProd, want: true},
		{name: "cluster-scoped row has no namespace", text: "namespace!=dev", row: node, want: false},
		{name: "age compares durations", text: "age>30m", row: apiProd, want: true},
		{name: "age below", text: "age<30m", row: apiProd, want: false},
		{name: "unknown age matches nothing", text: "age<30m", row: node, want: false},
		{name: "a column the row cannot know matches nothing", text: "cpu>0", row: apiProd, want: false},
		{name: "negated column term does not broaden", text: "cpu!=1", row: apiProd, want: false},
		{name: "name equality in any case", text: "name=API-1", row: apiDev, want: true},
		{name: "two name terms both apply", text: "api !dev/", row: apiDev, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f, err := models.ParseRowFilter(tc.text)
			if err != nil {
				t.Fatalf("ParseRowFilter(%q): %v", tc.text, err)
			}
			f.FaultsOnly = tc.faults
			if got := f.Matches(tc.row); got != tc.want {
				t.Errorf("Matches(%s) = %v, want %v", tc.row.ID, got, tc.want)
			}
		})
	}
}
