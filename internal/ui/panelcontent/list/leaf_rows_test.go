package list

import (
	"testing"

	"charm.land/lipgloss/v2"

	models "github.com/stokaro/kc/internal/models"
	table "github.com/stokaro/kc/internal/table"
)

func TestDecorateRowMarksLeaves(t *testing.T) {
	red := lipgloss.NewStyle().Foreground(lipgloss.Color("#FF5C5C"))
	leaf := models.NewSimpleItem("key", []string{"ca.crt", "x"}, nil, models.WhiteStyle())
	status := models.NewSimpleItem("bad", []string{"pod-1"}, nil, &red)
	dir := models.NewChildResourceItem("cm", []string{"/configmaps"}, nil, func() (models.Folder, error) { return nil, nil })

	_, cells, styles, _ := decorateRow(leaf).Columns()
	if cells[0] != " ca.crt" || cells[1] != "x" {
		t.Fatalf("leaf cells = %q, want indented name", cells)
	}
	if styles[0] != &leafStyle || styles[1] != &leafStyle {
		t.Fatalf("leaf cells should use the leaf style")
	}
	if _, orig, _, _ := leaf.Columns(); orig[0] != "ca.crt" {
		t.Fatalf("decoration must not modify the source row, got %q", orig[0])
	}

	_, _, styles, _ = decorateRow(status).Columns()
	if styles[0] != &red {
		t.Fatalf("status colors must be kept on leaves")
	}

	if _, cells, _, _ := decorateRow(dir).Columns(); cells[0] != "/configmaps" {
		t.Fatalf("enterable rows must stay unchanged, got %q", cells[0])
	}
	if _, cells, _, _ := decorateRow(models.BackItem{}).Columns(); cells[0] != "/.." {
		t.Fatalf("back row must stay unchanged, got %q", cells[0])
	}
}

var _ table.Row = leafRow{}

func TestDisplayListDoesNotModifyFolderRows(t *testing.T) {
	leaf := models.NewSimpleItem("key", []string{"ca.crt"}, nil, models.WhiteStyle())
	src := table.NewSliceList([]table.Row{leaf})
	dl := newDisplayList(src)
	if _, ok := dl.Lines(t.Context(), 0, 1)[0].(leafRow); !ok {
		t.Fatalf("display list should decorate leaves")
	}
	if _, ok := src.Lines(t.Context(), 0, 1)[0].(*models.SimpleItem); !ok {
		t.Fatalf("source rows must keep their type after decoration")
	}
}
