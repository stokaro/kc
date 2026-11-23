# 0190. Finding lifecycle: startup grace, duration, hysteresis, recovery

- Status: Accepted
- Date: 2026-11-23
- Issue: #190

## Context

Issue #190 asks health findings to show how long a state has lasted, to
tell a normal start or rollout from a persistent problem without hiding
hard failures, to stop measured-usage rules from flapping, to show
recovery for a while, to keep a denied or missing source from looking like
recovery, and to start over for a recreated object. It also asks for
grouping of repeated symptoms.

What the code had, checked on main: the overview engine
(`overview_engine.go`) recomputes a report from the shared cache at most
once a second; every rule (`overview_rules.go`, `overview_measured.go`) is a
pure function of the current input. A finding is a row with a count of
targets; a target is one object with a value and a severity. A source that
cannot be read makes the row `n/a`. Nothing remembers the previous report.
The object diagnosis (ADR 0189) reads the same kinds of state per object.

## Decision

### 1. Server-dated states get a startup grace in the rules

A new option `overview.startupGrace` (default 5m) applies to the
warning-level targets of four findings, dated only by the API server:

- `health/pods-not-ready`: a pod younger than the grace is dated by its
  creation. An older pod that lost readiness is dated by its Ready
  condition and gets no grace: it is not starting.
- `health/pods-pending`: dated by the pod's creation. Unschedulable and
  the image, configuration and crash-loop waiting reasons are explicit
  failures and do not wait.
- `health/workloads-unavailable`: a Deployment is dated by its
  `Available=False` condition. `Progressing=False` or `ReplicaFailure`
  bypasses the grace.
- `health/workloads-rollout`: a Deployment's rollout is dated by the
  creation of the newest ReplicaSet it controls (the controller creates it
  when the rollout starts). `Progressing=False` stays failed.

Inside the grace the target is `starting`: listed, with severity ok, so
the row keeps the color and faults-only behavior of a healthy row, and a
row whose only targets are starting has the state `starting`. After the
grace, and for explicit failures with a server date, the value says how
long the state has lasted (`for 12m`). StatefulSets and DaemonSets have no
dated condition for these states, so they get no grace: kc does not guess
when an undated state began. The F3 explanation of each graced finding
describes the grace and its value.

The object diagnosis uses the same option: a Deployment whose
`Available=False` condition is younger than the grace, with no failed
condition, is reported as info, "starting".

### 2. The engine keeps a session lifecycle per target

`overviewLifecycle` lives in the engine and reads each new report against
what it saw before. A target is keyed by finding, GVR, namespace, name and
UID, and is active when its severity is warning or worse. Only the Health
and Risks sections take part; Capacity lists every node whether or not it
is a problem.

- An active target with no server date that kc has seen for a minute or
  more says `seen 3m this session`.
- An active target that is gone from a finding that could be computed is
  listed as `recovered <age> ago (was: <value>)` with severity ok for ten
  minutes, at most twenty per finding. The row's value and severity remain
  those of the current input: they count active targets only.
- When a finding cannot be computed (its source is denied or not synced),
  its last active targets are listed as `unknown (<reason>); last seen ...`
  and stay active. A gap never resolves a finding. When the source returns,
  a target that is still there keeps its first sighting.
- A recreated object has a new UID, so its lifecycle starts fresh. The
  recovered row of the old UID is not shown while the same name is listed,
  so the targets list never has two rows with one ID.

### 3. Hysteresis for the container limit rules

`overview.usageHysteresisPercent` (default 5): a container flagged near its
memory limit or at its CPU limit stays flagged, with "held until below N%",
until its usage drops that many points below the warning percent. The rule
asks the lifecycle whether the target was active in the previous report,
through `computeOverviewWith`. `computeOverview` (one report, no session)
is unchanged for the existing callers.

## Deviations

- Grouping of repeated symptoms is not added to the overview. A repeated
  Warning event is one Event object whose count grows (or a series), which
  is already one target. Overlapping pod rules for one object (pending,
  image pull, not ready) are separate rows by design: each row is one rule
  with a count. The grouped, per-object view with every constituent reason,
  event and count is the `/diagnosis` of ADR 0189. Merging rows across
  rules would break the one-rule-one-row contract the counts, F3 and the
  targets lists rely on.
- One grace for startup and rollout, not one per condition: the four
  states have the same meaning (not converged yet), and one option is
  easier to reason about. Per-rule graces can be added if a cluster needs
  them.
- Hysteresis covers only the two container limit rules. The node capacity
  rows list every node and are judged by fixed bands; holding a node there
  would need the same session view in a rule that has no targets to key.
- The lifecycle is per engine, so it starts over when the overview engine
  is rebuilt (another context or namespace scope). It is not persisted.
- A zero `startupGrace` means the default, like every overview option; set
  `1s` to effectively disable the grace.

## Alternatives considered

- Keeping first-seen times for every target and using them for the grace.
  Rejected: the first sighting of a pod that has been stuck for an hour
  before kc started is not its start; treating it as starting would hide a
  persistent problem for the first minutes of every session.
- Lowering a starting target's severity to warning instead of ok. Rejected:
  the faults-only filter and the row colors would still flag a normal
  start, which is what the issue wants to avoid.
- Showing recovered targets in the row value. Rejected: the value is a count
  of the current problems, and a count that includes recovered objects
  would read as active problems.

## Consequences

- Rule tests that compute one report are unaffected; the session behavior
  is tested through `computeOverviewWith` and `apply` with a fixed clock.
- Values of graced and session-aged targets change over time, so the
  targets list refreshes as the engine recomputes.
- `overviewTarget` carries the UID, the server date and the starting flag;
  `config-default.yaml`, `appconfig.DefaultOverview` and the README sample
  have the two new options.
