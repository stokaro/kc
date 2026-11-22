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
	// namespace and pod name the pod of the container; empty when unknown.
	namespace, pod string
}

// SetPod records the pod the container belongs to.
func (c *ContainerItem) SetPod(namespace, pod string) {
	c.namespace, c.pod = namespace, pod
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
