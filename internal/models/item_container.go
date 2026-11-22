package models

import (
	"context"

	"charm.land/lipgloss/v2"
)

// ContainerSectionItem represents a category (containers/init/ephemeral) under a pod.
type ContainerSectionItem struct {
	*RowItem
	enter func() (Folder, error)
}

func NewContainerSectionItem(id string, cells, path []string, style *lipgloss.Style, enter func() (Folder, error)) *ContainerSectionItem {
	return &ContainerSectionItem{RowItem: NewRowItem(id, cells, path, style), enter: enter}
}

func (c *ContainerSectionItem) Enter() (Folder, error) {
	if c.enter == nil {
		return nil, nil
	}
	return c.enter()
}

// ContainerItem represents a concrete container entry.
type ContainerItem struct {
	*RowItem
	enter  func() (Folder, error)
	viewFn ViewContentFunc
	exec   *ExecTarget
	// namespace and pod name the pod of the container; empty when unknown.
	namespace, pod string
}

// SetPod records the pod the container belongs to.
func (c *ContainerItem) SetPod(namespace, pod string) {
	c.namespace, c.pod = namespace, pod
}

// SetExecTarget makes the row a target for shell and attach.
func (c *ContainerItem) SetExecTarget(t ExecTarget) { c.exec = &t }

// ExecTarget returns the container the row shows.
func (c *ContainerItem) ExecTarget(context.Context) (ExecTarget, error) {
	if c.exec == nil {
		return ExecTarget{}, ErrNoExecTarget
	}
	return *c.exec, nil
}

func NewContainerItem(id string, cells, path []string, style *lipgloss.Style, view ViewContentFunc, enter func() (Folder, error)) *ContainerItem {
	return &ContainerItem{RowItem: NewRowItem(id, cells, path, style), enter: enter, viewFn: view}
}

func (c *ContainerItem) Enter() (Folder, error) {
	if c.enter == nil {
		return nil, nil
	}
	return c.enter()
}

//revive:disable-next-line:function-result-limit the Viewable interface fixes these six results; internal/ui calls it
func (c *ContainerItem) ViewContent() (title, body, lang, mime, filename string, err error) {
	if c.viewFn == nil {
		return "", "", "", "", "", ErrNoViewContent
	}
	return c.viewFn()
}

// ContainerLogItem represents a log entry for a container.
type ContainerLogItem struct {
	*SimpleItem
	deps Deps
	spec LogsSpec
}

// NewContainerLogItem returns a log row for spec. Its view command is the
// kubectl command for spec.
func NewContainerLogItem(deps Deps, id string, cells, path []string, spec LogsSpec) *ContainerLogItem {
	item := NewSimpleItem(id, cells, path, GreenStyle())
	c := &ContainerLogItem{SimpleItem: item, deps: deps, spec: spec}
	c.SetViewCommand(c.LogsCommand(spec))
	return c
}

func (c *ContainerLogItem) LogsSpec() LogsSpec {
	return c.spec
}

// ContainerShellItem opens a shell in its container on Enter.
type ContainerShellItem struct {
	*SimpleItem
	target ExecTarget
}

func NewContainerShellItem(id string, cells, path []string, target ExecTarget) *ContainerShellItem {
	item := NewSimpleItem(id, cells, path, GreenStyle())
	return &ContainerShellItem{SimpleItem: item, target: target}
}

// ExecTarget returns the row's container.
func (c *ContainerShellItem) ExecTarget(context.Context) (ExecTarget, error) { return c.target, nil }

// ExecMode reports that Enter opens a shell.
func (c *ContainerShellItem) ExecMode() ExecMode { return ExecShell }

// LogsCommand returns the kubectl command for spec in this row's context.
func (c *ContainerLogItem) LogsCommand(spec LogsSpec) string {
	return logsCommand(c.deps, spec)
}
