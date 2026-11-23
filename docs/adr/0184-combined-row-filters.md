# 0184. Combined row filters

- Status: Accepted
- Date: 2026-11-23
- Issue: #184

## Context

The issue asks for one live view built from several predicates ("unready API
pods in these three namespaces on this node"). It lists name, label,
health/status, node and owner predicates, numeric comparisons on columns, a
selected-namespaces scope next to one namespace and all namespaces,
namespace-scoped collection for users without a cluster-wide list, a visible
and editable summary, and kubectl commands that say what they cannot express.

What the code did, checked on main:

- `ParseRowFilter` took exactly one of a name regex, `!regex` or `-l selector`.
  Faults only (`C-z`) was a separate flag ANDed with it.
- The filter runs on rows already in the panel: the live row source keeps all
  loaded rows and serves the ones the filter keeps, so it follows live updates.
- Row IDs in lists across namespaces are `namespace/name`, so same-named objects
  stay distinct for the cursor, marks and actions.
- A panel has one namespace scope: one namespace or all of them. A user who may
  not list namespaces can already type a namespace in the picker (#85).
- The copied kubectl command carried `-l` and silently dropped the name filter
  and faults only.

## Decision

1. **One text, terms separated by spaces, AND between all of them.** Spaces
   inside parentheses do not separate, so `-l env in (a, b)` and regex groups
   keep working. The old one-term filters (`nginx`, `!nginx`, `! nginx`,
   `-l <selector>`) parse and match as before.
2. **Terms:**
   - `regex` and `!regex` for the name (or `namespace/name`); several name terms
     all apply.
   - `-l <selector>` takes the longest run of terms that parses as one selector;
     several `-l` terms combine.
   - `-n a,b` is a shorthand for `namespace=a,b`.
   - `<column><op><value>` with `=`, `!=`, `>`, `>=`, `<`, `<=`. The column is its
     sort key from ADR 0037 (`cpu`, `mem`, `restarts`, `status`, `node`,
     `nominated-node`). `name`, `namespace` and `age` read the object's
     metadata.
3. **Typed comparisons reuse the sort's cell kinds** (ADR 0037): a column
   compares as quantities, counts, durations, ratios, numbers, IPs or text,
   decided over the whole column the way the sort decides. So `cpu>500m` compares
   millicores, `restarts>3` reads `4 (2m ago)` as 4, and `status=running` ignores
   case. A column's cells are prepared on first use, so filters without column
   terms cost nothing extra.
4. **Multiple values and missing data.** `=` keeps a row equal to any listed
   value; `!=` keeps a row equal to none of them. Ordering operators take one
   value. A missing cell (empty, `<none>`, `n/a`), a column the list lacks, a
   cluster-scoped row under a namespace term, or a value the column cannot hold
   matches nothing, `!=` included. A term never widens the view.
5. **Validation before applying.** The Filter dialog parses the text and asks the
   folder to check column terms against the columns of its last listing. An
   unknown column is refused with the list of known ones, and a value the column
   cannot hold is refused with the expected kind. Wide and added columns count,
   so hiding a column does not disable its term.
6. **Summary and removal.** The canonical text is the summary in the bottom
   border, as before. Editing the text in `M-f` changes or removes a term; a
   click on the label or an empty text clears the filter.
7. **kubectl command.** `-l` gets all selectors. A single `-n ns` in a list of all
   namespaces becomes kubectl's `-n ns`. Every other term, and faults only, is
   listed after the command: `# kc also filters: -n prod,stage, cpu>500m, faults`.

## Deviations from the issue

- **Selected namespaces is a filter on the all-namespaces list, not a new
  scope.** `-n a,b` narrows a list across namespaces and updates live through
  the same shared cache. A real multi-namespace scope (per-namespace caches
  merged into one list, for users who may not list across namespaces) touches
  navigation, the cluster pool and the RBAC checks. The overview lane may add
  namespace-scoped collection for #182; a multi-namespace scope should build on
  that, not on a second copy. Until then, a namespace-only user filters inside
  one namespace, which the picker can already enter by name.
- **No owner predicate.** "Resolved owner" means walking owner references
  (a Deployment through its ReplicaSets) and handling missing evidence. That is
  the relationships work (`M-j` owners). Doing it per row in a filter would
  duplicate it with a weaker version. A follow-up can add an `owner=` term once
  that resolution is a shared function.
- **Node and status use their columns** (`node=`, `status=`), not separate
  predicate kinds. Node is a wide column of the pod table and still filters in
  normal mode.
- **No new dialog with one field per category.** One text keeps the existing
  syntax, the frame summary and the copy-paste habit from k9s. It also avoids
  a form that would need a field per column.

## Alternatives considered

- **A structured form** (name, labels, namespaces, columns, faults). Easier to
  discover, but much more UI for the same predicates, and the summary would
  still be a text.
- **Splitting `-l` at the next space.** Breaks `env in (a, b)`. The
  longest-prefix rule keeps every selector that worked before.
- **Missing cells match `!=`.** Matches SQL-style intuition less than "unknown
  never matches", and would let `cpu!=0` widen a view to every pod without
  metrics.

## Consequences

- A regex with a space outside parentheses (`foo bar`) now means two name terms.
  Both must match, which is usually the intent; `foo\sbar` or `(foo bar)` keeps
  the old meaning.
- A term with an operator character (`a=b`) is a column term. Object names cannot
  hold those characters, so no name filter changes.
- Follow-ups: a multi-namespace scope on top of namespace-scoped collection
  (after #182), and an `owner=` term once owner resolution is shared.
