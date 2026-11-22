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

func NewContainerSectionItem(id string, cells []string, path []string, style *lipgloss.Style, enter func() (Folder, error)) *ContainerSectionItem {
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

func NewContainerItem(id string, cells []string, path []string, style *lipgloss.Style, view ViewContentFunc, enter func() (Folder, error)) *ContainerItem {
	return &ContainerItem{RowItem: NewRowItem(id, cells, path, style), enter: enter, viewFn: view}
}

func (c *ContainerItem) Enter() (Folder, error) {
	if c.enter == nil {
		return nil, nil
	}
	return c.enter()
}

func (c *ContainerItem) ViewContent() (string, string, string, string, string, error) {
	if c.viewFn == nil {
		return "", "", "", "", "", ErrNoViewContent
	}
	return c.viewFn()
}

// ContainerLogItem represents a log entry for a container.
type ContainerLogItem struct {
	*SimpleItem
	spec LogsSpec
}

func NewContainerLogItem(id string, cells []string, path []string, spec LogsSpec) *ContainerLogItem {
	item := NewSimpleItem(id, cells, path, GreenStyle())
	return &ContainerLogItem{SimpleItem: item, spec: spec}
}

func (c *ContainerLogItem) LogsSpec() LogsSpec {
	return c.spec
}

// ContainerShellItem opens a shell in its container on Enter.
type ContainerShellItem struct {
	*SimpleItem
	target ExecTarget
}

func NewContainerShellItem(id string, cells []string, path []string, target ExecTarget) *ContainerShellItem {
	item := NewSimpleItem(id, cells, path, GreenStyle())
	return &ContainerShellItem{SimpleItem: item, target: target}
}

// ExecTarget returns the row's container.
func (c *ContainerShellItem) ExecTarget(context.Context) (ExecTarget, error) { return c.target, nil }

// ExecMode reports that Enter opens a shell.
func (c *ContainerShellItem) ExecMode() ExecMode { return ExecShell }
