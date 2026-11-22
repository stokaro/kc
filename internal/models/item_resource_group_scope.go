package models

// listDeps returns the Deps whose cache counts this resource type and the
// scope decision behind it. The first call decides (access reviews, maybe a
// namespace cache from the pool) and must not run on the UI thread; later
// calls return the same answer until the item is closed.
func (r *ResourceGroupItem) listDeps() (Deps, listScope) {
	r.mu.Lock()
	if r.scopeKnown {
		d, s := r.scopedDeps, r.scope
		r.mu.Unlock()
		return d, s
	}
	base, gvr, ns := r.deps, r.gvr, r.namespace
	r.mu.Unlock()

	d, s, release := base.resolveListScope(gvr, ns)

	r.mu.Lock()
	if r.closed || r.scopeKnown {
		// Closed meanwhile, or another count decided first: keep one hold.
		if r.scopeKnown {
			d, s = r.scopedDeps, r.scope
		}
		r.mu.Unlock()
		release()
		return d, s
	}
	r.scopedDeps, r.scope, r.scopeRelease, r.scopeKnown = d, s, release, true
	r.mu.Unlock()
	return d, s
}

// listDepsLocked returns what listDeps decided, or the item's Deps before a
// decision. It never decides, so it is safe with r.mu held.
func (r *ResourceGroupItem) listDepsLocked() (Deps, listScope) {
	if r.scopeKnown {
		return r.scopedDeps, r.scope
	}
	return r.deps, scopeShared
}

// releaseScopeLocked forgets the scope decision and returns the release of
// its namespace cache, which the caller runs without r.mu held.
func (r *ResourceGroupItem) releaseScopeLocked() func() {
	release := r.scopeRelease
	r.scopedDeps, r.scope, r.scopeRelease, r.scopeKnown = Deps{}, scopeShared, nil, false
	if release == nil {
		return noScopeRelease
	}
	return release
}
