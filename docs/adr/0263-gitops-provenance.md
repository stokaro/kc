# 0263. GitOps owners, reconciliation evidence and desired-state compare

- Status: Accepted
- Date: 2026-11-23
- Issue: #263

## Context

Custom health rules ([0195](0195-custom-resource-health.md)) color Flux
and Argo CD objects, but nothing leads from a live workload to the
GitOps object that applied it, its source and revision, or tells an
operator why a manual edit will be reverted. The issue asks for a
read-only Flux adapter first, explicit evidence for every link, and a
desired/live comparison only from an explicitly supplied rendered
object, never "in sync" by default.

## Decision

1. **The GitOps manager is an owner.** `M-j` (owners) lists, after the
   owner references, the manager the object's metadata names:
   - Flux Kustomization: labels `kustomize.toolkit.fluxcd.io/name` and
     `/namespace`;
   - Flux HelmRelease: `helm.toolkit.fluxcd.io/name` and `/namespace`;
   - Argo CD Application: annotation `argocd.argoproj.io/tracking-id`,
     else label `argocd.argoproj.io/instance`.

   `app.kubernetes.io/instance` is not read: Helm and charts set it, so
   it proves nothing. A tracking id that names another object (copied
   with a manifest) is shown as such and not followed, as Argo CD does.
   An object with a claim and no owner reference now has an owners
   folder; before, `M-j` said "no owner".

2. **Labels are a claim, the manager's record is the evidence.** The row
   says `listed in its inventory` when the Kustomization's
   `status.inventory` has the object's id
   (`<namespace>_<name>_<group>_<Kind>`), `listed in its
   status.resources` for an Application, and `label only: ...`
   otherwise: not listed (pruned from the source, or labels copied), no
   record, or a HelmRelease, which keeps no object list. managedFields
   are not used: a field manager name says who wrote, not what owns.
   Inventories record names, not UIDs, so a deleted and recreated
   object of the same name is still listed; that matches what the
   controller will do with it.

3. **Reconciliation evidence lives in `/diagnosis`** of Kustomizations,
   HelmReleases and Applications, in any served version, rendered like
   every other diagnosis. For Flux: suspend, status generation behind
   the spec, Ready/Stalled/Reconciling with reason, message and time,
   applied versus attempted revision, the source (sourceRef, chartRef or
   chart.spec.sourceRef) with its Ready condition and artifact revision,
   source revision ahead of the applied one, and `spec.dependsOn`
   targets that are missing or not ready. The object's own manager is
   in the summary, so nested Kustomizations can be followed up. For
   Argo CD: sources, sync status and revision with `reconciledAt`,
   health, failed operation and conditions. OutOfSync is shown as Argo
   CD's comparison with its time, not as kc's.

   A configured `health:` entry for these types no longer gives them the
   generic custom-resource diagnosis; the GitOps one replaces it.

4. **Drift is unavailable without a desired object.** Every GitOps
   diagnosis carries a `drift-unchecked` finding and a note: a Ready
   condition means the last apply succeeded, not that live objects
   match Git. kc does not clone repositories, fetch artifacts or run
   kustomize or helm.

5. **Desired/live comparison through an explicit local file.** `C-x C-d`
   accepts a local file (from a local-files panel, `M-l`) on one side
   and a live object on the other. kc picks the file's one document for
   that object (API group, resource of the kind, name, namespace or
   none), reuses the manifest diff and its normalization, and names both
   origins and revisions in the header: the file's SHA-256 and mtime,
   the live object's resourceVersion. The header already lists the
   fields normalization omits. No document, or several, is an error.

6. **Reads.** Everything goes through the related folders' scoped
   reader ([0056](0056-namespace-scoped-access.md)): a denied type gives
   `forbidden` or an `incomplete` finding, never an empty answer. The
   owners folder watches each claimed manager by name. The diagnosis
   watches the object and its own type in the namespace; sources are
   read on each refresh but not watched, because the controller writes
   the object's status when its source changes.

## Deviations from the issue

- No "configured provider" for desired objects. A local file covers the
  explicit case; a provider that renders on demand would run tools or
  fetch Git, which the issue itself rules out silently doing.
- Argo CD gets the same owner and diagnosis support at a smaller depth
  instead of being an extension point only, because its metadata and
  status are as easy to read as Flux's and the lane asked for both.
- One cluster-wide read: an Argo CD claim without a namespace in the
  tracking id (the Application lives in Argo CD's own namespace, which
  the object does not record) lists Applications in all namespaces when
  the owners folder opens, through the shared cache if the user may.
  Two matches are reported as ambiguous. It is not watched.
- Flux sources are not diagnosed on their own; their Ready condition
  and revision appear in the diagnosis of the objects that use them.

## Consequences

- A new `gitops.go` with the claims and owner rows, `gitops_diagnosis.go`
  with the diagnosis, and `compare_local.go` in the UI for the file side
  of `C-x C-d`. `manifestdiff.Side` gains an optional `Revision`.
- Fixtures cover nested dependencies, absent and stale inventories,
  missing and forbidden managers and sources, Flux not installed,
  ambiguous Applications, copied tracking ids and a differing desired
  file, plus an envtest run against installed Flux CRDs.
