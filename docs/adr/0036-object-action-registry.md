# 0036. CronJob actions through a built-in object-action registry

- Status: Accepted
- Date: 2026-11-22
- Issue: #36

## Context

Issue #36 asks for "Run now" (trigger) and "Suspend/Resume" on CronJobs, in
"the built-in action section proposed in #33". Neither #33 (scale, restart,
rollback) nor #34 (cordon, drain) exists yet, so this issue is the first to
need built-in object actions, and it sets the shape that the other two will
reuse. Open questions:

1. Where the actions live in the UI.
2. How actions are found for an object, and how #33 and #34 add theirs.
3. How the confirmation flow runs API work, since the existing
   `models.ConfirmedAction.Confirm` runs on the Bubble Tea update goroutine.
4. What exactly "Run now" creates.
5. How Suspend/Resume knows which of the two to offer.
6. What "select the new Job in the opposite panel" means when that panel is
   busy.

## Decision

1. **F9 menu, top section.** The F9 menu gets an "Object Actions" section
   above the configured commands, shown when exactly one object is selected
   in a list. F9 is now enabled when there are built-in actions even without
   custom commands. No new key bindings.

2. **Registry keyed by full GVR** (`internal/objectaction`). An
   `objectaction.Action` has an ID, a menu label, the API verb it needs on
   the object's resource, and a `Prepare` function.
   `Registry.Register(gvr, actions...)` adds actions; `Registry.For(gvr,
   supports)` returns them in order, hiding those whose verb the row's
   discovery verbs do not include. `objectaction.Default()` registers all
   built-ins; #33 and #34 add a `registerXActions` call there. Keys are full
   GVRs (`batch/v1/cronjobs`) as AGENTS.md requires, so a CRD named
   `cronjobs` in another group never gets these actions.

3. **Two-step, off the update goroutine.** Selecting an action runs
   `Prepare` in a `tea.Cmd`: it reads the object from the shared cache (as
   unstructured, so it shares the informer the viewer uses) and returns a
   `models.ClusterAction` with a `Confirmation` and `Run(ctx)`. The UI shows
   the red dialog; on approval it runs `Run` in another `tea.Cmd` and shows
   the result on the command line. `models.Confirmation` gained a `Command`
   field that the dialog prints under "Same as:", so every action shows its
   kubectl equivalent. The objects come from the listing folder through
   `models.ActionEnvProvider` (implemented by `ObjectsFolder`), so a
   namespace-scoped panel uses its namespace-scoped cache and the kubectl
   command gets `--context` when needed.

4. **Run now mirrors `kubectl create job --from=cronjob/<name>`**: the job
   template's spec, labels and annotations, the annotation
   `cronjob.kubernetes.io/instantiate: manual`, and an owner reference to the
   CronJob with `controller: true`. kubectl needs a name; kc picks
   `<cronjob>-manual-<5 random chars>` before the dialog opens, so the dialog
   and the kubectl command name the exact Job. The CronJob part is cut so the
   name fits in 63 characters (the `job-name` label limit). The Job is built
   from the template the user saw when confirming, not re-read.

5. **One menu entry, "Suspend/Resume...".** The menu is built on the update
   goroutine and must not read the API, so the label cannot depend on
   `spec.suspend`. `Prepare` reads it and the dialog says "Suspend CronJob?"
   or "Resume CronJob?". The change is a merge patch that sets
   `spec.suspend` to the shown target value, so a repeat sends the same
   state instead of flipping it back.

6. **The opposite panel follows only from a list.** After Run now, the other
   panel opens the Jobs list in the Job's namespace and selects the Job,
   retrying while the informer catches up (same budget as the startup
   selection). If that panel shows a viewer, describe, manifest or a
   command, it is left alone. Focus stays on the source panel.

## Alternatives considered

- **A free function key (F6) or k9s-style letter keys** (`t`, `s`). As in mc,
  typed letters go to the shell line, and F6 is reserved for rename/move. A menu section scales to #33 and #34 without new keys.
- **Actions as config commands** (`kubectl create job ...` templates in
  `config.yaml`). They cannot read the object first, cannot confirm with
  the object's state, and cannot select the result.
- **Actions declared by folders or rows** (each folder returns its own
  actions). Every folder that lists CronJobs, Deployments or Nodes would
  have to repeat the wiring; a GVR-keyed registry keeps the actions in one
  place and the folder only supplies the client.
- **Run `ConfirmedAction.Confirm` in the background for all actions.** The
  podfs helper consent row relies on `Confirm` running in place; a separate
  `ClusterAction` keeps that row unchanged.
- **`generateName` for the Job.** The dialog and the kubectl command could
  not name the Job, and kubectl itself requires an explicit name.
- **Two entries, "Suspend" and "Resume", or a label from the table's
  Suspend column.** Two entries offer a no-op; the table column depends on
  the server's printer and on the column mode.
- **A typed `batchv1.CronJob` read.** It would start a second, typed
  informer next to the unstructured one.

## Consequences

- #33 and #34 add an action set to `objectaction.Default()` and reuse the
  dialog, the busy indicator, the result message and "select the created
  object". Scale needs a number dialog, and drain needs options and several
  selected nodes; both need extensions (an input step before `Prepare`, and
  multi-object targets) that this registry does not have yet.
- Verb gating checks the verbs of the object's own resource only. Run now
  needs `create` on Jobs, which the CronJob row cannot tell; a missing
  permission shows as an error after confirming.
- The CronJob is read from the cache, so a change made a moment before may
  not be visible yet. The dialog shows what kc saw, and that is what runs.
- Messages from a finished action are dropped if another dialog is open when
  they arrive, as for delete.
