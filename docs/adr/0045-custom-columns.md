# 0045. User-defined columns per resource type

- Status: Accepted
- Date: 2026-11-22
- Issue: #45

## Context

Object lists show the server-side Table columns only, in normal or wide mode
(`objects.columns`). Users want extra columns (the image of a Deployment, the
node IP of a Pod) and want to drop columns they never read. k9s does this in
`views.yaml` with JSONPath columns per GVR.

The issue left these questions open:

1. The config shape and its key.
2. The expression language.
3. Where the object comes from: the Table cache asks the server for
   `includeObject=metadata`, so rows carry no spec or status.
4. Whether user columns replace or extend the server columns, and how to hide
   server columns.
5. How this works with wide mode.
6. How bad expressions are reported.
7. What the kubectl command line shows.

## Decision

### 1. Config shape

A top-level `columns` map, keyed by `group/version/resource` as AGENTS.md
requires for identity (`gvr.GroupVersion().String() + "/" + resource`, so
`apps/v1/deployments`, and `v1/pods` for the core group). Each value is a list
of entries:

```yaml
columns:
  apps/v1/deployments:
    - name: IMAGE
      jsonPath: .spec.template.spec.containers[*].image
    - name: Up-to-date
      hide: true
```

An entry has `name`, and either `jsonPath` (add a column) or `hide: true`
(hide the server column of that name). `wide: true` on an added column shows
it only in wide mode. The default is no entries (`columns: {}` in
`config-default.yaml`, nil in `Default()`, omitted on save).

A key matches one version only. `apps/v1beta1/deployments` is a different
key, the same as for folder keys and row IDs.

### 2. Expression language

kubectl custom-columns JSONPath, evaluated with
`k8s.io/client-go/util/jsonpath` the way `kubectl get -o custom-columns` does
it: the relaxed forms `.a.b`, `a.b`, `{.a.b}`, `{a.b}`, missing keys allowed,
all matches joined with `,`, `<none>` when nothing matches, terminal escape
sequences escaped. The relaxed-form regexp is copied from
`k8s.io/kubectl/pkg/cmd/get/customcolumn.go`; importing that package would
pull kubectl's command tree and several new modules into the binary.

### 3. Where the object comes from

The full object is read from the shared controller-runtime cache through
`Cluster.ListByGVR`, which serves the unstructured informer for the GVR. Every
open object list already starts this informer for its change events
(`startInformerForResource`), so the lookup adds no watch and no API request.
It runs only for a GVR that has added columns, in the background list load,
bounded to 5 seconds; if the informer is not synced yet, the cells stay empty
and the sync event reloads the list.

The Table cache keeps `includeObject=metadata`.

### 4. Extend, hide, replace

Added columns are appended after the server columns. `hide: true` drops a
server column by name, ignoring case. `name: "*"` with `hide: true` drops all
server columns except Name, so the list shows Name plus the added columns
(replace). Name is never hidden: kc uses it for the row title, the child
marker and identity.

### 5. Wide mode

Wide mode keeps working as before for server columns (priority > 0 columns
appear). Hidden columns are hidden in both modes. Added columns show in both
modes unless they set `wide: true`.

### 6. Error reporting

The whole `columns` config is validated when an object list loads (bad keys,
empty names, `jsonPath` together with `hide`, `hide` on Name, `wide` on a
hide entry, expressions that do not parse). All problems go into one message
on the command line (`Deps.ReportError`), and the same message is shown once
per process. A column with a bad expression stays in the table, empty, so the
user sees where it is. An expression that parses but fails on an object (for
example indexing a number) is reported once per GVR and column, and its
cells stay empty. Nothing is reported per row.

### 7. kubectl command

When added columns are visible, the shown command gets
`-o custom-columns=NAME:.metadata.name[,NAMESPACE:.metadata.namespace],<columns>`.
With replace (`*` hidden) this is the same listing. Otherwise the command ends
with `# kc also shows the server columns`, since custom-columns cannot print
server Table columns. kubectl splits the spec at `,` and `:`, so a column
whose name has `:` or `,`, or whose expression has `,`, cannot be written;
the command then has no `-o` and says `# kc adds custom columns`.

## Alternatives considered

- **Ask the Table cache for full objects** (`includeObject=Object`) for GVRs
  with added columns. Rejected: it changes the shared Table cache, keeps a
  second copy of every object in Table rows, and the full objects are already
  in the unstructured informer.
- **Put the columns under `objects.columns`**. Rejected: that key is already
  the string `normal`/`wide`; changing its type would break stored configs.
- **A per-GVR object** (`{replace: true, hide: [...], add: [...]}`). It is
  more explicit, but the flat list matches k9s and kubectl custom-columns and
  keeps the common case (add one column) to one entry. The `*` hide entry
  covers replace.
- **CEL or Go templates** as the expression language. Rejected: users know
  JSONPath from `kubectl get -o custom-columns` and `-o jsonpath`, and
  client-go already ships it.
- **Fail config loading on a bad entry**. Rejected: kc would not start over a
  typo in a column; the rest of the config is fine.
- **Report evaluation errors per row**. Rejected by the issue: one bad
  expression would flood the command line.
- **Namespace-specific columns** (k9s supports `group/version/resource@ns`).
  Left out; the key format leaves room for a suffix later.

## Consequences

- Added columns are text, so sorting by value (PR #82) treats them like any
  other cell.
- Lists without configured columns behave as before; the merge step in
  `internal/models/custom_columns.go` is a no-op for a nil plan.
- A user column titled `Age` gets the live age hook that server Age columns
  get, because the hook is chosen by title.
- Not done here: the column on/off list in F2 Options that the issue
  proposes, column alignment, and a default sort column. They need UI work and
  can build on this config.
