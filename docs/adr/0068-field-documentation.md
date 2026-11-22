# 0068. Field documentation from the cluster's OpenAPI v3 schema

- Status: Accepted
- Date: 2026-11-22
- Issue: #68

## Context

Users browsing unfamiliar objects, CRDs in particular, have to leave kc to
look up what a field means. The issue asks for field descriptions, types,
required status, nested fields and enum/default values, read from the
connected cluster for the selected API version, loaded in the background
with bounded caching, and a copyable `kubectl explain` command. It leaves
open where the data comes from, how it is cached, what the UI looks like,
which key opens it, and whether the field under the cursor in the manifest
viewer should be followed.

Constraints: no new dependency; no API work on the Bubble Tea update
goroutine; an INFO log line before every real request; full
group/version identity everywhere. Open pull requests (#78, #83) rewrite
the Manifest and Describe widgets and the viewer, and #80 adds F9 object
actions, so this change should not touch those files.

## Decision

1. **Data source: `/openapi/v3`, one document per group-version.** kc
   reads the discovery index at `/openapi/v3` and then the document of the
   row's group-version, the same source `kubectl explain` uses. The kind's
   schema is the one whose `x-kubernetes-group-version-kind` matches group,
   version and kind exactly; a kind name alone is not unique (a CRD may
   define `Deployment` in its own group). The version is the one the panel
   lists (the row's GVR), not the kind's preferred version. kc parses the
   few schema keywords it shows itself (`$ref`, single-element `allOf`,
   `properties`, `required`, `items`, `additionalProperties`, `enum`,
   `default`, `format` and a few `x-kubernetes-*` extensions) instead of
   pulling in kube-openapi's spec types. The client is client-go's
   `openapi.NewClientWithContext` over a plain discovery REST client, not
   the memory-cached discovery client, so kc controls caching itself.

2. **Caching: bounded LRU keyed by cluster and group-version, validated by
   the index hash.** `explain.Loader` keeps the 16 most recently used parsed
   documents. The key is the API server host plus the cluster key without
   namespace and selector scope (they share one server). Every open reads
   the small index first: each entry's URL carries a hash that changes with
   the schema, and a cached document is used only when the hash matches.
   CRD changes and context switches therefore show up on the next `M-e`
   without a timer or a watch. Cost per open: one small GET when cached,
   two GETs otherwise.

3. **UI shape: a schema tree as a kc folder in the same panel.** `M-e`
   pushes `/explain/<group>/<version>/<kind>` onto the active panel. Rows
   have Name (`/field` for fields with nested fields), Type (kubectl style:
   `string`, `[]Container`, `map[string]string`, `IntOrString`), Required
   and the first sentence of the description. `Enter` descends; arrays and
   maps are crossed to their items or values, as in kubectl. `F3` opens the
   full documentation of a field (description, enum, default, format, list
   type, child fields) in the normal viewer. `..` goes up, and from the
   kind back to the row the user started from, with the cursor restored.
   The folder tells the navigator that its path is the whole breadcrumb
   (`StandalonePath`), because it is not below the row it was opened from.

4. **Key: `M-e`, with `Esc e` as fallback.** It works on object rows and
   resource type rows; "e" reads as "explain". F9 stays the custom command
   menu. On 2026-11-22 neither main nor any open pull request used `M-e`
   or `Esc e`. Already taken: `M-c`, `M-a`, `M-s`, `M-w` (main), `M-o`,
   `M-r` (#82), `M-y`, `M-u`, `M-H`, `C-\` (#90), `M-f`, `C-z` (#93) and
   `M-p` (#91).

5. **Equivalent command.** The folder's command line shows
   `kubectl explain <resource>[.<field>...] --api-version=<group/version>`
   (plus `--context` when needed), and each field's viewer shows the
   command for that field. kubectl takes the group from `--api-version`,
   so the resource argument is the plain plural.

6. **States.** The request runs in a `tea.Cmd` behind the busy spinner. If
   the group-version is not in the index or the document has no schema for
   the kind, the folder shows one dim row "No documentation ...". A failed
   request (forbidden, network, bad JSON) shows a red row "Schema request
   failed: ..." and a toast. A schema without properties (for example
   `x-kubernetes-preserve-unknown-fields`) shows "No fields documented". The
   user always gets a folder to go back from. If the panel has moved to
   another folder by the time the schema arrives, kc does not push it and
   says so.

## Alternatives considered

- **OpenAPI v2 (`/openapi/v2`).** One large document for the whole
  cluster, and the server strips CRD schema constructs v2 cannot express.
  `kubectl explain` moved to v3 for these reasons. Rejected.
- **CRD objects (`apiextensions.k8s.io`) for custom resources.** Works only
  for CRDs, needs list/get on CRDs, and would make built-ins a separate
  path. `/openapi/v3` covers both with one code path. Rejected.
- **kubectl's `explain` package (`k8s.io/kubectl/pkg/explain/v2`).** It
  renders text from templates; kc needs structured fields for rows. Reusing
  only its traversal rules (which this change mirrors) was enough.
- **Discovery's memory cache for the OpenAPI client.** It keeps the index
  until discovery is invalidated, so a CRD update could be missed. The hash
  check gives freshness at the cost of one small request per open.
- **A side pane that follows the cursor in the manifest viewer (lfk
  style).** Attractive, but it needs a YAML position to field path mapping
  inside the Manifest widget, which open PRs rewrite. Deferred; the
  `explain.Node.Lookup(path)` API is what such a pane would call.
- **An entry in the F9 menu.** The F9 menu lists configured shell
  commands; adding built-in actions is the subject of #80. A dedicated key
  avoids that dependency. The action can be added to F9 once built-in
  actions exist there.
- **Opening the tree in the opposite panel.** It would replace what the
  other panel shows. Pushing onto the active panel keeps both panels as
  they were and makes "return to the resource" a plain `..`.

## Consequences

- Built-in kinds and CRDs use one code path; a test runs it against a real
  kube-apiserver (envtest) with a CRD whose kind clashes with
  `apps/v1 Deployment`.
- Each `M-e` costs one request even when cached. A parsed built-in
  document is a few MiB, so the cache is bounded by count (16), not by
  bytes.
- Aggregated APIs that publish no OpenAPI v3 show "No documentation".
- Follow-ups: follow the field under the cursor in the Manifest view once
  #78 lands; offer Explain in the F9 menu once #80 adds built-in actions;
  a recursive view (`kubectl explain --recursive`) as a viewer mode.
