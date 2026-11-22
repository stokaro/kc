package models

// podContainerRef names the container a ContainerItem stands for.
type podContainerRef struct {
	namespace string
	pod       string
	container string
}

// SetPodContainer records the pod and container the row stands for, so
// views that follow the selection, such as the Logs panel mode, can find
// them without parsing the row path.
func (c *ContainerItem) SetPodContainer(namespace, pod, container string) {
	c.ref = podContainerRef{namespace: namespace, pod: pod, container: container}
}

// PodContainer returns the namespace, pod and container of the row. All are
// empty when SetPodContainer was not called.
func (c *ContainerItem) PodContainer() (namespace, pod, container string) {
	return c.ref.namespace, c.ref.pod, c.ref.container
}
