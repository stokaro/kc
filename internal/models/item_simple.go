package models

import "charm.land/lipgloss/v2"

// SimpleItem embeds RowItem and can optionally expose view content.
type SimpleItem struct {
	*RowItem
	viewFn ViewContentFunc
}

func NewSimpleItem(id string, cells, path []string, style *lipgloss.Style) *SimpleItem {
	return &SimpleItem{RowItem: NewRowItem(id, cells, path, style)}
}

func (s *SimpleItem) WithViewContent(fn ViewContentFunc) *SimpleItem {
	s.viewFn = fn
	return s
}

//revive:disable-next-line:function-result-limit the Viewable interface fixes these six results; internal/ui calls it
func (s *SimpleItem) ViewContent() (title, body, lang, mime, filename string, err error) {
	if s.viewFn == nil {
		return "", "", "", "", "", ErrNoViewContent
	}
	return s.viewFn()
}
