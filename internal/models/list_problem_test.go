package models

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	schema "k8s.io/apimachinery/pkg/runtime/schema"

	table "github.com/stokaro/kc/internal/table"
)

// problemFolder is a folder over a live source that loads on the calling
// goroutine, as without Deps.
func problemFolder(t *testing.T, populate func(context.Context) ([]table.Row, error)) (*BaseFolder, *liveObjectRowSource) {
	t.Helper()
	f := NewBaseFolder(Deps{Ctx: t.Context()}, []table.Column{{Title: " Name"}}, nil)
	src := newLiveObjectRowSourceWithHooks(populate, f.markDirtyFromSource, nil)
	src.setTarget(liveSourceTarget{gvr: schema.GroupVersionResource{Version: "v1", Resource: "pods"}, namespace: "shop"})
	f.SetRowSource(src)
	t.Cleanup(src.Close)
	return f, src
}

func TestListProblemOfALiveFolder(t *testing.T) {
	refused := errors.New("dial tcp 127.0.0.1:6443: connect: connection refused")
	forbidden := apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "", errors.New("denied"))
	tests := []struct {
		name  string
		loads []error // the result of each load; nil lists one row
		want  error
	}{
		{name: "a failed first load is the problem", loads: []error{refused}, want: refused},
		{name: "a forbidden list is the problem", loads: []error{forbidden}, want: forbidden},
		{name: "a load that listed rows has none", loads: []error{nil}},
		{name: "a load that recovered clears it", loads: []error{refused, nil}},
		{name: "a failed refresh keeps the rows and is no problem", loads: []error{nil, refused}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var load atomic.Int32
			f, src := problemFolder(t, func(context.Context) ([]table.Row, error) {
				err := tt.loads[min(int(load.Add(1))-1, len(tt.loads)-1)]
				if err != nil {
					return nil, err
				}
				return oneRow("web-1"), nil
			})
			ctx := t.Context()
			for range tt.loads {
				src.MarkDirty()
				f.Len(ctx)
			}
			if n := int(load.Load()); n != len(tt.loads) {
				t.Fatalf("loads = %d, want %d", n, len(tt.loads))
			}
			if got := f.ListProblem(); !errors.Is(got, tt.want) || (got == nil) != (tt.want == nil) {
				t.Errorf("ListProblem() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestListProblemNamesAMissingNamespace(t *testing.T) {
	missing := apierrors.NewNotFound(schema.GroupResource{Resource: "namespaces"}, "shpo")
	tests := []struct {
		name      string
		rows      []table.Row
		checked   bool // whether the namespace check runs
		wantFound bool // whether ListProblem names the namespace
	}{
		{name: "an empty list asks and names it", rows: nil, checked: true, wantFound: true},
		{name: "a list with rows does not ask", rows: oneRow("web-1"), checked: false, wantFound: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f, src := problemFolder(t, func(context.Context) ([]table.Row, error) { return tt.rows, nil })
			var checks atomic.Int32
			src.namespaceCheck = func(context.Context) error {
				checks.Add(1)
				return missing
			}
			f.Len(t.Context())
			if got := checks.Load() > 0; got != tt.checked {
				t.Errorf("namespace checked = %t, want %t", got, tt.checked)
			}
			got := f.ListProblem()
			if found := apierrors.IsNotFound(got); found != tt.wantFound {
				t.Errorf("ListProblem() = %v, want the missing namespace: %t", got, tt.wantFound)
			}
		})
	}
}

// A namespace that exists by the next load is no longer named.
func TestListProblemForgetsANamespaceThatAppeared(t *testing.T) {
	f, src := problemFolder(t, func(context.Context) ([]table.Row, error) { return nil, nil })
	var exists atomic.Bool
	src.namespaceCheck = func(context.Context) error {
		if exists.Load() {
			return nil
		}
		return apierrors.NewNotFound(schema.GroupResource{Resource: "namespaces"}, "shop")
	}
	ctx := t.Context()
	f.Len(ctx)
	if err := f.ListProblem(); !apierrors.IsNotFound(err) {
		t.Fatalf("ListProblem() = %v before the namespace exists, want NotFound", err)
	}
	exists.Store(true)
	src.MarkDirty()
	f.Len(ctx)
	if err := f.ListProblem(); err != nil {
		t.Errorf("ListProblem() = %v after the namespace exists, want nil", err)
	}
}

func TestListProblemOfAPopulatedFolder(t *testing.T) {
	refused := errors.New("connection refused")
	var fail atomic.Bool
	fail.Store(true)
	f := NewBaseFolder(Deps{Ctx: t.Context()}, []table.Column{{Title: " Name"}}, nil)
	f.SetPopulate(func(context.Context) ([]table.Row, error) {
		if fail.Load() {
			return nil, refused
		}
		return oneRow("a"), nil
	})
	ctx := t.Context()
	f.Len(ctx)
	if err := f.ListProblem(); !errors.Is(err, refused) {
		t.Fatalf("ListProblem() = %v after a failed populate, want %v", err, refused)
	}
	fail.Store(false)
	f.Refresh()
	f.Len(ctx)
	if err := f.ListProblem(); err != nil {
		t.Errorf("ListProblem() = %v after a populate that listed a row, want nil", err)
	}
}
