package models

// PodContainer returns the namespace, pod and container of the row, so views
// that follow the selection, such as the Logs panel mode, can find them
// without parsing the row path. All are empty when SetPod was not called.
func (c *ContainerItem) PodContainer() (namespace, pod, container string) {
	if c.pod == "" {
		return "", "", ""
	}
	return c.namespace, c.pod, c.ID()
}
