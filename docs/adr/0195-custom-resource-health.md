# 0195. Health of custom resources from configuration

- Status: Accepted
- Date: 2026-11-23
- Issue: #195

## Context

Issue #195 asks for declarative adapters, keyed by full GVR, that map the
status of a custom resource into kc's health and workload views: condition
mappings, reason and message paths, an observedGeneration path, desired
and ready counts, pod relationships by owner chain or selector, one
interpreted result for row colors, the faults filter, /diagnosis and the
Info summaries, startup validation, and documented precedence.

What the code had, checked on main:

- `row_health.go` classifies rows through a fixed map of built-in GVRs
  (pods, nodes, claims, volumes, Deployments, StatefulSets, ReplicaSets,
  DaemonSets, Jobs). Every other type is unknown. The row stylers and the
  faults filter (ADR 0038) both call `ClassifyRowHealth`, so they agree.
- `/diagnosis` (ADR 0189) explains one workload from the shared cache,
  matching pods by controller owner UID. It exists for the built-in
  workload types only.
- The overview's Info summaries and its workload grouping
  (`overview_workloads.go`, `overview_info_workload.go`) are computed from
  the overview report, built from cluster-wide informers for the whole
  overview. The overview lane is changing that code (#221, #173).
- `columns` (custom columns) already shows the shape of a per-type config:
  keys `group/version/resource`, kubectl custom-columns JSONPath
  expressions, errors reported once.

## Decision

### 1. A `health` map of rules, keyed like `columns`

```yaml
health:
  kustomize.toolkit.fluxcd.io/v1/kustomizations:
    - condition: Stalled
      health: failed
    - condition: Reconciling
      health: notReady
    - condition: Ready
      health: ok
    - condition: Ready
      status: "False"
      health: notReady
  argoproj.io/v1alpha1/applications:
    - jsonPath: .status.health.status
      value: Healthy
      health: ok
```

A rule matches a status condition (`condition`, with `status` True by
default) or one field (`jsonPath` and `value`). Rules are tried in order and
the first match sets the health: `ok`, `succeeded`, `notReady` or
`failed`, the states rows already have. No match leaves the object
unknown. A key names one version: another version of the same type has no
rules.

The vocabulary is the one kc already has. Condition rules cover kstatus
types (Flux, Cluster API, many kubebuilder operators) and Crossplane's
Synced and Ready. Field rules use the kubectl custom-columns JSONPath of
`columns`, read leniently: a missing field matches nothing. There is no
predicate language or script.

`status` and `value` accept YAML booleans and numbers, so `status: True`
works unquoted (YAML reads it as a boolean; kc writes it back as `True`).

### 2. Generation lag is read, not configured

A verdict counts as stale when `metadata.generation` is greater than
`status.observedGeneration` or than the matched condition's
`observedGeneration`, whichever is present (the older of the two is the
one shown). A stale `ok` or `succeeded` becomes `notReady`, so the row is
yellow and the faults filter keeps it; a stale `failed` stays `failed`.
`/diagnosis` says "The status does not reflect the current spec yet" with
both numbers.

These two fields are where the Kubernetes API conventions and kstatus put
the observed generation. A configurable path was left out: a mistyped
path would silently turn the check off, and it can be added as a rule
field when an operator needs it.

### 3. Wrong types are errors, missing fields are not

`status.conditions` that is not a list, an `observedGeneration` that is
not an integer, or a JSONPath that gives an object or a list makes the
verdict unknown with an adapter error. `/diagnosis` shows the error as an
incomplete finding. Absent fields and condition entries without a string
`type` just match nothing.

### 4. Validated at startup; built-in rules win

kc validates the config when it starts, in `App.Init`, and installs what is
valid. Problems go to the log and to the command line, as custom commands
do. An entry with any bad rule is left out whole, so a half-read entry never
decides a health. Errors: a bad key, no rules, an unknown health, a rule
with both or neither of condition and jsonPath, a bad status, a bad
JSONPath, and a rule that matches what an earlier rule of the same type
already matches (it would never apply; `.a` and `{.a}` are the same path).
This is the "overlapping adapters" check: one key is one type, so two
entries can only overlap inside one rule list.

An entry for a type with a built-in rule is reported and ignored. The
built-in rules know about replicas, rollouts and container states that a
condition rule cannot express, and letting config replace them would
make the meaning of a red Deployment row depend on the user's file.

### 5. One verdict for rows, the faults filter and /diagnosis

`ClassifyRowHealth` falls back to the installed rules, so the row colors
and the faults filter (C-z) read the same verdict. A configured namespaced
type gets the `/diagnosis` row of ADR 0189. Its summary states the health,
the rule that decided it, the field it matched and the condition's reason;
a failed or not ready verdict is a finding with the condition's reason,
message and transition time; a stale status, an adapter error and "no rule
matches" are findings of their own. The pods, events and claims checks are
the existing ones.

The rules are installed in a package-level atomic pointer, like the row
stylers, because rows are styled without a `Deps`. kc loads the config once
at start, so one install per run is the only write.

### 6. Pods: the object's own, by controller UID

A configured type's diagnosis lists the pods whose controller owner
reference has the object's UID, the same match ADR 0189 uses. A recreated
object with the same name never inherits pods, and pods with similar names
are never related. Pods of objects the custom resource creates (a
StatefulSet, a Deployment) are not followed; the diagnosis note says so.
CloudNativePG Clusters and other operators that create pods directly are
covered. A deeper owner chain needs reads of more types per diagnosis and
is left for later.

Cluster-scoped configured types get row colors but no `/diagnosis`: their
pods and events would need cluster-wide reads, which kc avoids.

## Deviations from the issue

- **Info summaries and overview workload grouping are not extended.** They
  come from the overview report and its cluster-wide informers, which
  another lane is changing. Row colors, the faults filter and `/diagnosis`
  share one verdict, which meets the "one interpreted result" goal for
  the views that look at one object.
- **No configured reason, message or observedGeneration paths.** Reason and
  message come from the matched condition; a field rule reports the field
  and its value. The generation is read from the two standard places.
- **No desired/ready counts and no selector-based pod relation.** A
  selector would relate pods by label, which is the "similar name" risk
  the issue warns about in another form; counts need a second vocabulary.
  Both can be added as fields of a rule list entry later without changing
  the format.
- **No built-in adapters.** The default is empty. A plausible default for
  a CRD the user may not run, or runs at another version, would color rows
  by guesswork. The README and `config-default.yaml` show Flux and Argo CD
  examples; the tests use Flux, Crossplane and Argo CD layouts.
- **cert-manager.** The overview's certificate rule (ADR 0051) is not
  touched. A `cert-manager.io/v1/certificates` entry with a Ready rule
  colors Certificate rows; it is an example, not a replacement.

## Consequences

- Users of operators can color rows, filter faults and read a diagnosis of
  their own types with a few lines of config and no code.
- A config error never changes the health of a type: the entry is left out
  and reported.
- No new watches: rows color from the list's own informer, and /diagnosis
  watches the object, the pods, the events and the claims of its namespace,
  as for a built-in workload.
- Health state names in the config (`notReady`) follow the kc row states,
  not any operator's vocabulary.
