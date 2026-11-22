package ui

// updatedModel returns the model that an Update call returned, as the type
// of the current one. The models in this package return themselves from
// Update, so a model of another type keeps the current one.
func updatedModel[T any](current T, next any) T {
	if m, ok := next.(T); ok {
		return m
	}
	return current
}
