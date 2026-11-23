package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/stokaro/kc/internal/models"
	modeltesting "github.com/stokaro/kc/internal/models/testing"
	nav "github.com/stokaro/kc/internal/navigation"
	table "github.com/stokaro/kc/internal/table"
)

// selectionFolder gives the row "inputs" its own command line and leaves
// every other row to the folder's command.
type selectionFolder struct {
	models.Folder
}

func (selectionFolder) KubectlCommand() string { return "kubectl get pods -n web" }

func (selectionFolder) CommandLineFor(item models.Item) (models.CommandLine, bool) {
	if id, _, _, _ := item.Columns(); id != "inputs" {
		return models.CommandLine{}, false
	}
	return models.CommandLine{Label: "Inputs (2)", Show: "kubectl top nodes  # +1 more", Copy: "# a\nkubectl top nodes\n# b\nkubectl describe nodes\n", What: "2 input commands"}, true
}

func TestCommandLineFollowsTheSelectedRow(t *testing.T) {
	a := NewApp()
	ctx := t.Context()
	rows := []table.Row{
		models.NewSimpleItem("section", []string{"Capacity"}, nil, nil),
		models.NewSimpleItem("inputs", []string{"/nodes cpu"}, nil, nil),
	}
	folder := selectionFolder{Folder: modeltesting.NewStaticFolder("overview", []table.Column{{Title: " Name"}}, rows)}
	a.leftNav = nav.NewNavigator(folder)
	a.rightNav = nav.NewNavigator(mkFolder("right"))
	a.leftPanel.UseFolder(ctx, true)
	a.leftPanel.SetFolder(ctx, folder, false)
	a.activePanel = 0
	a.width = 100

	a.leftPanel.SelectByRowID(ctx, "section")
	if line := a.panelCommandLine(); line.Label != "Command" || line.Show != "kubectl get pods -n web" {
		t.Fatalf("section row: %+v", line)
	}

	a.leftPanel.SelectByRowID(ctx, "inputs")
	line := a.panelCommandLine()
	if line.Label != "Inputs (2)" || !strings.HasPrefix(line.Copy, "# a\n") {
		t.Fatalf("inputs row: %+v", line)
	}
	if plain := ansi.Strip(a.renderCommandLine()); !strings.HasPrefix(plain, " Inputs (2): kubectl top nodes") {
		t.Fatalf("rendered %q", plain)
	}
}
