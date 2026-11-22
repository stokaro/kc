package models

var _ ActionEnvProvider = (*ObjectsFolder)(nil)

// ActionEnv lets built-in actions work on the objects this folder lists.
// It uses the folder's cluster, so a namespace-scoped folder reads through
// its namespace-scoped cache.
func (o *ObjectsFolder) ActionEnv() (ActionEnv, bool) {
	if o == nil || o.BaseFolder == nil || o.Deps.Cl == nil {
		return ActionEnv{}, false
	}
	deps := o.Deps
	return ActionEnv{
		Client:  deps.Cl.GetClient(),
		Context: deps.CtxName,
		Kubectl: func(args ...string) string { return kubectlCommand(deps, args...) },
	}, true
}
