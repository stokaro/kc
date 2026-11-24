package navigation_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stokaro/kc/internal/models"
	modeltesting "github.com/stokaro/kc/internal/models/testing"
	navui "github.com/stokaro/kc/internal/navigation"
	"github.com/stokaro/kc/internal/table"
)

// hidingFolder lists some rows and knows more, like a root that hides empty
// resource types.
type hidingFolder struct {
	*models.BaseFolder
	hidden map[string]models.Item
}

func (f *hidingFolder) ExplicitItemByID(_ context.Context, id string) (models.Item, bool) {
	item, ok := f.hidden[id]
	return item, ok
}

func TestGoToEntersHiddenRows(t *testing.T) {
	t.Parallel()
	style := models.WhiteStyle()
	podsList := modeltesting.NewStaticFolder("pods", nil, nil)
	jobsList := modeltesting.NewStaticFolder("jobs", nil, nil)
	betaJobsList := modeltesting.NewStaticFolder("jobs-beta", nil, nil)
	row := func(id string, next models.Folder) *modeltesting.EnterableItem {
		return modeltesting.NewEnterableItem(id, []string{"/" + id}, []string{id}, func() (models.Folder, error) { return next, nil }, style)
	}
	newRoot := func() *hidingFolder {
		return &hidingFolder{
			BaseFolder: modeltesting.NewStaticFolder("/", nil, []table.Row{row("ns//v1/pods", podsList)}),
			hidden: map[string]models.Item{
				"ns/batch/v1/jobs":      row("ns/batch/v1/jobs", jobsList),
				"ns/batch/v1beta1/jobs": row("ns/batch/v1beta1/jobs", betaJobsList),
			},
		}
	}
	tests := []struct {
		name     string
		folder   func() models.Folder
		id       string
		want     models.Folder
		notFound bool
	}{
		{name: "listed row", folder: func() models.Folder { return newRoot() }, id: "ns//v1/pods", want: podsList},
		{name: "hidden row", folder: func() models.Folder { return newRoot() }, id: "ns/batch/v1/jobs", want: jobsList},
		{name: "hidden row keeps its version", folder: func() models.Folder { return newRoot() }, id: "ns/batch/v1beta1/jobs", want: betaJobsList},
		{name: "same resource in another group", folder: func() models.Folder { return newRoot() }, id: "ns/example.com/v1/jobs", notFound: true},
		{name: "unserved version", folder: func() models.Folder { return newRoot() }, id: "ns/batch/v2/jobs", notFound: true},
		{
			name: "folder without hidden rows",
			folder: func() models.Folder {
				return modeltesting.NewStaticFolder("/", nil, []table.Row{row("ns//v1/pods", podsList)})
			},
			id:       "ns/batch/v1/jobs",
			notFound: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			nav := navui.NewNavigator(tc.folder())
			_, err := navui.GoTo(t.Context(), nav, []navui.GoToStep{{SelectionID: tc.id, Enter: true}})
			if tc.notFound {
				var nf *navui.ItemNotFoundError
				if !errors.As(err, &nf) || nf.ID != tc.id {
					t.Fatalf("GoTo(%q) error = %v, want not found", tc.id, err)
				}
				if nav.HasBack() {
					t.Fatalf("GoTo(%q) entered %v", tc.id, nav.Current().Path())
				}
				return
			}
			if err != nil {
				t.Fatalf("GoTo(%q): %v", tc.id, err)
			}
			if nav.Current() != tc.want {
				t.Fatalf("GoTo(%q) is at %v, want %v", tc.id, nav.Current().Path(), tc.want.Path())
			}
		})
	}
}
