# 0319. Which resource types a type catalog lists

- Status: Accepted
- Date: 2026-11-24
- Issue: #319

## Context

A cluster with a few operators serves well over a hundred resource
types. kc's root, the resources of a namespace and the cluster
resources list every discovered type that supports list and watch. The
only filter is `resources.showNonEmptyOnly`, which hides empty types
and types whose emptiness is not known yet; across all namespaces it
hides nothing, because kc does not count there. `resources.order:
favorites` changes the order, not what is listed.

Issue #319 asks for a compact default (Work), a profile that hides
chosen types, a profile that lists only chosen types, and a way to
reach everything else. Kubernetes discovery has no "system" or
"internal" flag. API group, CRD categories, scope, owner references,
Helm labels, verbs and object counts do not tell a daily API from an
implementation detail: ApplicationSet-generated Applications are
primary objects, a CertificateRequest can be created by hand, and a
Crossplane managed resource or a customer CRD can be the main API of a
team.

This record covers the policy: what each mode lists and from which
data. How the folders apply it (the Other types entry, Browse all,
lazy counts), the editor and the configuration keys come in later
pull requests of #319; a change to what is decided here gets a new
record that supersedes this one.

## Decision

1. **Work is a curated working set.** `internal/visibility` holds a
   small list of exact group and plural resource names
   (`WorkCatalog`), each with a short reason and a primary source, and
   a version (`WorkCatalogVersion`, now 1). The list is the table of
   #319: the core workload, configuration, storage and cluster types;
   Deployments, StatefulSets, DaemonSets, Jobs, CronJobs, Ingresses,
   NetworkPolicies, HPAs, PDBs, StorageClasses; and the daily APIs of
   cert-manager, Argo CD, Flux, Prometheus Operator, Gateway API,
   Cilium and Calico. A type outside the list is "not in Work", never
   "system". Nothing joins Work by heuristic: not by `categories: all`,
   owner, group name, scope or popularity. Users add types themselves.

2. **Modes.** With D the discovered types of the catalog's scope that
   kc can browse, W the Work set, and Show and Hide the profile's
   selectors:

   | Mode | Listed | A newly discovered type |
   |---|---|---|
   | work (default) | D ∩ ((W ∪ Show) − Hide) | listed if in Work |
   | all | D | listed |
   | hide | D − Hide | listed unless hidden |
   | only | D ∩ Show | not listed until selected |

   A profile does not inherit another profile's or Work's choices.
   An empty only profile lists no types; it never means all. A selector
   may not be both shown and hidden; `hide` takes no Show list, `only`
   no Hide list, `all` neither. `Evaluate` returns an error for an
   invalid profile, so a broken only profile cannot silently become
   all; the caller decides what to show instead and says so.

3. **Selectors are exact group and resource.** A selector is a
   `schema.GroupResource` (group `""` for core), checked as a DNS label
   and a DNS subdomain, with no wildcards and no version. It matches
   whatever version discovery prefers, so a preferred-version change
   keeps the user's intent. It is a policy matcher only: rows, folders,
   history and the hotlist keep the full group, version and resource
   (#318).

4. **Two known aliases, written out.** Work lists one Events API: core
   `events` when it is served, else `events.k8s.io` `events`. The rule
   is a field on that one catalog entry (`OnlyWithout`), not a match on
   equal Kinds. Hiding core events does not bring the other one in.
   All, hide and only see both as distinct types. Calico's public
   `projectcalico.org` policies are in Work; the
   `crd.projectcalico.org` backing types are not, and missing public
   discovery does not promote them.

5. **Explicit selections are marked.** A Show selector of a work
   profile and a member of an only profile are `Explicit`: the catalog
   lists them even when they are empty or still counting, while the
   non-empty filter keeps applying to the other rows. Explicit grants
   no access; a forbidden type still says so.

6. **Selectors that match nothing are kept.** `Missing` lists them, so
   an editor can mark them "not currently discovered" instead of
   dropping them when a CRD is reinstalled or discovery is partial.
   `Other` lists the discovered types left out; its length is the N of
   "Other types (N)", a number of types, not objects.

The policy reads only the discovered names. It lists no objects and
starts no watch, so it can run before a folder starts counts.

## Alternatives considered

- **Classify by heuristics** (CRD categories, owner references, group
  suffixes such as `.k8s.io`, cluster scope). Each one misfiles common
  cases named in #319, and a misfiled type is hidden without the user
  knowing why.
- **Make favorites the allow-list.** Favorites order the catalog and
  match bare plural names; turning them into a filter would change
  existing configurations and confuse same-named types of different
  groups.
- **Selectors with a version or with wildcards.** A version breaks on
  every preferred-version change; a group wildcard silently captures
  APIs installed later. Bulk selection in the editor can instead store
  the exact types it matched.
- **Merge equal Kinds generally.** Two groups often serve the same Kind
  name for different things (NetworkPolicy in three groups in a Calico
  cluster). Only the Events alias is known to be one API.

## Consequences

- The Work list is code. Changing it means raising
  `WorkCatalogVersion`; a new version never rewrites a user's own
  selections.
- A cluster whose main APIs are not in the list starts with them under
  Other. The later pull requests make Other one Enter away and adding a
  type to Work one key.
- This change adds the package only; no folder uses it yet, so kc's
  behavior does not change in this pull request.
