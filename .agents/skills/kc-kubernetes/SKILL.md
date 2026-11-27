---
name: kc-kubernetes
description: Implement or review kc's Kubernetes clients, discovery, shared caches, leases, namespace scope, RBAC, per-panel contexts, or resource actions. Use for target identity and API semantics; skip purely visual TUI changes, generic Go edits, and prose.
---

# Kubernetes changes in kc

Paths below are relative to the repository root. Identify the target connection,
resource identity, cache owner, and request lifetime before editing. Read the
relevant decision, including its supersession notice; old ADRs can describe
the former single-cluster application.

## Target identity and actions

- Each panel owns a `panelConn`; followers and pins use their effective target.
  Follow [ADR 0362](../../../docs/adr/0362-independent-panel-contexts.md) and
  [panel_conn.go](../../../internal/ui/panel_conn.go). Capture the connection
  when work starts; a context name alone cannot distinguish switching away
  and back. Discard stale replies and release any lease they acquired.
- Carry source connection, full GVR, namespace, and name through selections,
  lookup keys, reads, and actions. Include UID when binding a destructive
  confirmation to the object instance. Never reconstruct a target from the
  active panel or the resource name alone.
- Keep read-only and authorization decisions attached to the target context.
  Recheck the relevant policy when confirming; preserve UID preconditions so
  a replacement object cannot be deleted under an older confirmation.
- Distinguish missing, forbidden, disconnected, and empty data. Preserve
  namespace-scoped access; a failed cluster-wide list must not erase known
  permitted namespaces or silently broaden the requested scope.

## Cache and request lifecycle

- Use the existing controller-runtime/client-go clients, RESTMapper, and shared
  informer/table cache. Avoid parallel ad-hoc watches and duplicate wrappers.
- Hold acquired scopes with leases and release them on every exit path.
  See [cache ownership](../../../docs/adr/0062-cache-ownership.md), whose former
  app-wide ownership is superseded by ADR 0362. Navigation owns folder cleanup;
  pins and streams may outlive the panel that first acquired a connection.
- Treat cached objects as shared; copy before changing them. Preserve GVR and
  selector scope in cache keys. Do not implement refresh by mutating cache data.
- Pass cancellation through API calls and streams. Log real outbound requests
  with the existing context logger and target metadata. Check whether an
  apparently local lookup can call discovery before invoking it on the UI
  goroutine; use `kc-tui` for asynchronous delivery.

## Choose the test that can prove the behavior

Use fake clients and injected clocks for pure routing and ownership logic.
Use envtest for RBAC propagation, discovery/CRDs, watch/cache behavior, API
validation, UID/resourceVersion semantics, and interactions between contexts;
fake object stores do not establish those server guarantees.

- Start control planes through `internal/testing/kcenvtest`. Reuse the package
  control plane unless a test needs isolated cluster-wide state or a second
  cluster. Use `kctesting.UniqueName` for shared namespaces/users/objects.
- Start cluster workers with `kctesting.RunInBackground`; after granting RBAC,
  use `kctesting.WaitForAccess` before kc reads and caches an access decision.
- For multi-context work, use identical namespace/name pairs in two envtest
  servers. Check the actual target, stale completion, and lease cleanup.
- Run `make test-unit PACKAGES='./internal/cluster'` for pure pool logic or
  `make test-integration PACKAGES='./internal/ui'` for panel integration,
  substituting the actual changed packages. Add targeted race checks when
  ownership or concurrency changes. Never use a real cluster, even read-only;
  check running pods, streams and controllers on a
  [throwaway kind cluster](../../../docs/agent-workflow.md#throwaway-kind-clusters).

Reference tests: [pool lifetimes](../../../internal/cluster/pool_test.go),
[two-context actions and switches](../../../internal/ui/panel_contexts_envtest_test.go),
and [envtest under load](../../../docs/adr/0244-envtest-under-load.md).
