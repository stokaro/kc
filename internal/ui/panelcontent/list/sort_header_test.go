package list

import (
	"fmt"
	"testing"

	models "github.com/stokaro/kc/internal/models"
	table "github.com/stokaro/kc/internal/table"
	panelcontent "github.com/stokaro/kc/internal/ui/panelcontent"
)

type sortableFolder struct {
	orderCaptureFolder
	cols []table.Column
	mark models.SortMark
	ok   bool
}

func (f *sortableFolder) Columns() []table.Column           { return f.cols }
func (f *sortableFolder) SortMark() (models.SortMark, bool) { return f.mark, f.ok }
func (f *sortableFolder) SortKeys() []string                { return []string{"name", "age"} }
func (f *sortableFolder) ObjectOrder() string               { return "-age" }

func TestHeaderColumnsMarksSortedColumn(t *testing.T) {
	t.Parallel()
	cols := []table.Column{{Title: " Name"}, {Title: "Age"}}
	cases := []struct {
		name string
		f    models.Folder
		want []string
	}{
		{"descending", &sortableFolder{cols: cols, mark: models.SortMark{Column: 1, Desc: true}, ok: true}, []string{" Name", "Age↓"}},
		{"ascending", &sortableFolder{cols: cols, mark: models.SortMark{Column: 0}, ok: true}, []string{" Name↑", "Age"}},
		{"no shown column", &sortableFolder{cols: cols}, []string{" Name", "Age"}},
		{"out of range", &sortableFolder{cols: cols, mark: models.SortMark{Column: 5}, ok: true}, []string{" Name", "Age"}},
		{"not sortable", &orderCaptureFolder{}, []string{" Name", "Age"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := columnsToTitles(headerColumns(tc.f, cols))
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Errorf("titles = %q, want %q", got, tc.want)
			}
			if cols[1].Title != "Age" || cols[0].Title != " Name" {
				t.Fatalf("headerColumns changed the folder's columns: %q", columnsToTitles(cols))
			}
		})
	}
}

func TestSortStateReportsFolderSort(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	w := New(panelcontent.WidgetDeps{})
	w.UseFolder(true)
	w.SetFolder(ctx, &sortableFolder{cols: []table.Column{{Title: " Name"}, {Title: "Age"}}}, false)
	st, ok := w.SortState()
	if !ok {
		t.Fatal("SortState not ok for a sortable folder")
	}
	if got := fmt.Sprintf("%v %v %s", st.Keys, st.Titles, st.Applied); got != "[name age] [Name Age] -age" {
		t.Errorf("SortState = %s", got)
	}
	w.SetFolder(ctx, &orderCaptureFolder{}, false)
	if _, ok := w.SortState(); ok {
		t.Error("SortState ok for a folder that cannot sort")
	}
}
