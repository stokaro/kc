package models

// refLink is an ObjectLink to an object whose identity is already known,
// for content outside a folder that names an object, such as a top pod of
// the Info panel mode.
type refLink struct {
	*RowItem
	ref ObjectRef
}

var _ ObjectLink = refLink{}

// LinkTarget returns the object.
func (l refLink) LinkTarget() (ObjectRef, string) { return l.ref, "" }

// NewObjectRefLink returns a link that OpenObjectRequest opens in the other
// panel: ref with its full GVR, namespace and name.
func NewObjectRefLink(ref ObjectRef) ObjectLink {
	id := ref.GVR.GroupVersion().String() + "/" + ref.GVR.Resource + "/" + ref.Namespace + "/" + ref.Name
	return refLink{RowItem: NewRowItem(id, []string{ref.Name}, nil, nil), ref: ref}
}

// Ref is the pod as an object reference.
func (c InfoConsumer) Ref() ObjectRef {
	return ObjectRef{GVR: podsGVR, Namespaced: true, Namespace: c.Namespace, Name: c.Name}
}
