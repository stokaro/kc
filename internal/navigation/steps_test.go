package navigation_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/stokaro/kc/internal/models"
	modeltesting "github.com/stokaro/kc/internal/models/testing"
	"github.com/stokaro/kc/internal/navigation"
	"github.com/stokaro/kc/internal/table"
)

// tree builds root -> a -> b, each reached through an enterable row.
func tree() models.Folder {
	style := models.WhiteStyle()
	leaf := modeltesting.NewStaticFolder("a/b", nil, nil)
	mid := modeltesting.NewStaticFolder("a", nil, []table.Row{
		modeltesting.NewEnterableItem("b", []string{"b"}, []string{"a", "b"}, func() (models.Folder, error) { return leaf, nil }, style),
	})
	return modeltesting.NewStaticFolder("/", nil, []table.Row{
		modeltesting.NewEnterableItem("a", []string{"a"}, []string{"a"}, func() (models.Folder, error) { return mid, nil }, style),
	})
}

func TestNavigatorStepsReplay(t *testing.T) {
	t.Parallel()
	nav := navigation.NewNavigator(tree())
	if got := nav.Steps(); got != nil {
		t.Fatalf("root steps = %v, want none", got)
	}
	if _, err := navigation.GoTo(t.Context(), nav, []navigation.GoToStep{{SelectionID: "a", Enter: true}, {SelectionID: "b", Enter: true}}); err != nil {
		t.Fatalf("GoTo: %v", err)
	}
	steps := nav.Steps()
	if !slices.Equal(steps, []string{"a", "b"}) {
		t.Fatalf("steps = %v, want [a b]", steps)
	}

	replay := navigation.NewNavigator(tree())
	var goSteps []navigation.GoToStep
	for _, s := range steps {
		goSteps = append(goSteps, navigation.GoToStep{SelectionID: s, Enter: true})
	}
	if _, err := navigation.GoTo(t.Context(), replay, goSteps); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !slices.Equal(replay.Current().Path(), []string{"a", "b"}) {
		t.Fatalf("replay ended at %v", replay.Current().Path())
	}
}

func TestNavigatorBackTo(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		depth int
		want  []string
	}{
		{"keeps all", 2, []string{"a", "b"}},
		{"one level up", 1, []string{"a"}},
		{"root", 0, nil},
		{"never pops the root", -1, nil},
		{"deeper than the stack", 5, []string{"a", "b"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			nav := navigation.NewNavigator(tree())
			if _, err := navigation.GoTo(t.Context(), nav, []navigation.GoToStep{{SelectionID: "a", Enter: true}, {SelectionID: "b", Enter: true}}); err != nil {
				t.Fatalf("GoTo: %v", err)
			}
			nav.BackTo(tc.depth)
			if got := nav.Steps(); !slices.Equal(got, tc.want) {
				t.Fatalf("steps = %v, want %v", got, tc.want)
			}
			if nav.Current() == nil {
				t.Fatalf("navigator lost its root")
			}
		})
	}
}

func TestGoToMissingRowIsItemNotFound(t *testing.T) {
	t.Parallel()
	nav := navigation.NewNavigator(tree())
	res, err := navigation.GoTo(t.Context(), nav, []navigation.GoToStep{{SelectionID: "a", Enter: true}, {SelectionID: "missing", Enter: true}})
	var notFound *navigation.ItemNotFoundError
	if !errors.As(err, &notFound) || notFound.ID != "missing" {
		t.Fatalf("err = %v, want ItemNotFoundError for missing", err)
	}
	if len(res.Entered) != 1 {
		t.Fatalf("entered %d folders before the missing row, want 1", len(res.Entered))
	}
}
