package modeltesting

import (
	"strings"

	lipgloss "charm.land/lipgloss/v2"

	models "github.com/stokaro/kc/internal/models"
)

// EnterableItem is a reusable Item that is also Enterable via a provided function.
type EnterableItem struct {
	*models.RowItem
	enter  func() (models.Folder, error)
	viewFn models.ViewContentFunc
}

var _ models.Item = (*EnterableItem)(nil)
var _ models.Enterable = (*EnterableItem)(nil)

func NewEnterableItem(id string, cells, path []string, enter func() (models.Folder, error), style *lipgloss.Style) *EnterableItem {
	if len(cells) > 0 && !strings.HasPrefix(cells[0], "/") {
		cloned := append([]string(nil), cells...)
		cloned[0] = "/" + cloned[0]
		cells = cloned
	}
	return &EnterableItem{RowItem: models.NewRowItem(id, cells, path, style), enter: enter}
}

func NewEnterableItemStyled(id string, cells, path []string, styles []*lipgloss.Style, enter func() (models.Folder, error)) *EnterableItem {
	if len(cells) > 0 && !strings.HasPrefix(cells[0], "/") {
		cloned := append([]string(nil), cells...)
		cloned[0] = "/" + cloned[0]
		cells = cloned
	}
	return &EnterableItem{RowItem: models.NewRowItemStyled(id, cells, path, styles), enter: enter}
}

func (e *EnterableItem) Enter() (models.Folder, error) {
	if e.enter == nil {
		return nil, nil
	}
	return e.enter()
}

func (e *EnterableItem) WithViewContent(fn models.ViewContentFunc) *EnterableItem {
	e.viewFn = fn
	return e
}

//revive:disable-next-line:function-result-limit the signature is the models.Viewable interface
func (e *EnterableItem) ViewContent() (title, body, lang, mime, filename string, err error) {
	if e.viewFn == nil {
		return "", "", "", "", "", models.ErrNoViewContent
	}
	return e.viewFn()
}
