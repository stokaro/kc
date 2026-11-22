package models

import "charm.land/lipgloss/v2"

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
	deps Deps
	spec LogsSpec
}

// NewContainerLogItem returns a log row for spec. Its view command is the
// kubectl command for spec.
func NewContainerLogItem(deps Deps, id string, cells []string, path []string, spec LogsSpec) *ContainerLogItem {
	item := NewSimpleItem(id, cells, path, GreenStyle())
	c := &ContainerLogItem{SimpleItem: item, deps: deps, spec: spec}
	c.SetViewCommand(c.LogsCommand(spec))
	return c
}

func (c *ContainerLogItem) LogsSpec() LogsSpec {
	return c.spec
}

// LogsCommand returns the kubectl command for spec in this row's context.
func (c *ContainerLogItem) LogsCommand(spec LogsSpec) string {
	return logsCommand(c.deps, spec)
}
