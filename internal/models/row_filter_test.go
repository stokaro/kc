package models_test

import (
	"testing"

	"github.com/stokaro/kc/internal/models"
)

func TestParseRowFilter(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		wantErr  bool
		wantText string
		wantSel  string
		active   bool
	}{
		{name: "empty", text: "", active: false},
		{name: "blank", text: "   ", active: false},
		{name: "regex", text: "ngin.x", wantText: "ngin.x", active: true},
		{name: "trimmed", text: "  web  ", wantText: "web", active: true},
		{name: "inverse", text: "!web", wantText: "!web", active: true},
		{name: "inverse with space", text: "! web", wantText: "! web", active: true},
		{name: "inverse without pattern", text: "!", wantErr: true},
		{name: "bad regex", text: "web(", wantErr: true},
		{name: "bad inverse regex", text: "!web[", wantErr: true},
		{name: "label selector", text: "-l app=web", wantText: "-l app=web", wantSel: "app=web", active: true},
		{name: "label selector canonical", text: "-l   app=web,tier!=db", wantText: "-l app=web,tier!=db", wantSel: "app=web,tier!=db", active: true},
		{name: "label set selector", text: "-l env in (prod, dev)", wantText: "-l env in (prod, dev)", wantSel: "env in (dev,prod)", active: true},
		{name: "label exists", text: "-l app", wantText: "-l app", wantSel: "app", active: true},
		{name: "empty selector", text: "-l", wantErr: true},
		{name: "bad selector", text: "-l =web", wantErr: true},
		{name: "dash pattern is a regex", text: "-log", wantText: "-log", active: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f, err := models.ParseRowFilter(tc.text)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseRowFilter(%q) = nil error, want an error", tc.text)
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
			if got := f.Active(); got != tc.active {
				t.Errorf("Active() = %v, want %v", got, tc.active)
			}
		})
	}
}

func TestRowFilterMatches(t *testing.T) {
	web := models.RowFacts{Name: "web-1", ID: "web-1", Labels: map[string]string{"app": "web", "tier": "front"}, Health: models.RowHealthOK}
	db := models.RowFacts{Name: "db-0", ID: "data/db-0", Labels: map[string]string{"app": "db"}, Health: models.RowHealthNotReady}
	job := models.RowFacts{Name: "migrate", ID: "migrate", Health: models.RowHealthFailed}
	done := models.RowFacts{Name: "backup", ID: "backup", Health: models.RowHealthSucceeded}

	tests := []struct {
		name   string
		text   string
		faults bool
		row    models.RowFacts
		want   bool
	}{
		{name: "no filter keeps all", row: db, want: true},
		{name: "substring", text: "web", row: web, want: true},
		{name: "case insensitive", text: "WEB", row: web, want: true},
		{name: "anchored miss", text: "^eb", row: web, want: false},
		{name: "regex", text: `^web-\d$`, row: web, want: true},
		{name: "inverse hides match", text: "!web", row: web, want: false},
		{name: "inverse keeps miss", text: "!web", row: db, want: true},
		{name: "namespace in id", text: "^data/", row: db, want: true},
		{name: "inverse namespace in id", text: "!^data/", row: db, want: false},
		{name: "selector match", text: "-l app=web", row: web, want: true},
		{name: "selector miss", text: "-l app=web", row: db, want: false},
		{name: "selector set", text: "-l app in (web,db)", row: db, want: true},
		{name: "selector not exists", text: "-l !tier", row: web, want: false},
		{name: "selector no labels", text: "-l app", row: job, want: false},
		{name: "faults keep not ready", faults: true, row: db, want: true},
		{name: "faults keep failed", faults: true, row: job, want: true},
		{name: "faults hide ok", faults: true, row: web, want: false},
		{name: "faults hide succeeded", faults: true, row: done, want: false},
		{name: "faults and name", text: "db", faults: true, row: db, want: true},
		{name: "faults and name miss", text: "web", faults: true, row: db, want: false},
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
				t.Errorf("Matches(%+v) = %v, want %v", tc.row, got, tc.want)
			}
		})
	}
}

func TestRowFilterLabel(t *testing.T) {
	tests := []struct {
		name   string
		text   string
		faults bool
		want   string
	}{
		{name: "none", want: ""},
		{name: "text", text: "!web", want: "!web"},
		{name: "faults", faults: true, want: "faults"},
		{name: "both", text: "-l app=web", faults: true, want: "-l app=web, faults"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f, err := models.ParseRowFilter(tc.text)
			if err != nil {
				t.Fatal(err)
			}
			f.FaultsOnly = tc.faults
			if got := f.Label(); got != tc.want {
				t.Errorf("Label() = %q, want %q", got, tc.want)
			}
			if got := f.Active(); got != (tc.want != "") {
				t.Errorf("Active() = %v, want %v", got, tc.want != "")
			}
		})
	}
}
