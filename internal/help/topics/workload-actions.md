# Workload actions

kc can scale, restart and roll back workloads, and run or suspend a
CronJob, without leaving the panel. Each change shows what kc read and
the equivalent kubectl command in a red dialog before anything is sent.

## Where to find them

Enter a workload (press `Enter` on its row). Below its child types are
one row per action:

- Deployment: `Scale...`, `Restart...`, `Roll back...`
- StatefulSet: `Scale...`, `Restart...`
- DaemonSet: `Restart...`
- ReplicaSet: `Scale...`

`Enter` on the row starts the action. These rows exist only for the
built-in `apps/v1` types, not for a custom resource with the same name.

For a CronJob, the `F2` user menu starts with `Run now...` and
`Suspend/Resume...`, above the custom commands.

## How it works

- Scale opens a value dialog, `Replicas for web:`, filled with the
  current count read from the scale subresource. Type a whole number,
  0 or more, and press `Enter`.
- Restart sets the pod template annotation
  `kubectl.kubernetes.io/restartedAt`, as `kubectl rollout restart`.
  The workload replaces its pods as its update strategy says.
- Roll back opens a pick list of the Deployment's revisions, newest
  first. Each line shows the revision number, its ReplicaSet and its
  images; the current one is marked `current` and cannot be chosen.
  The newest older revision is preselected, the one
  `kubectl rollout undo` would pick.
- Run now creates a Job from the CronJob's template, named
  `<cronjob>-manual-<suffix>`, as `kubectl create job --from=cronjob/`.
  The other panel then shows the new Job.
- Suspend/Resume sets `spec.suspend`. The dialog says which of the two
  it does.

Every action then opens the red confirmation dialog with the context,
namespace, object and the change (`Replicas: 3 -> 0`, `From revision`
and `To revision`), notes on side effects, and the kubectl command.

Notes to read in the dialog:

- Scaling to 0 stops every pod of the workload.
- A HorizontalPodAutoscaler may change the count again.
- A ReplicaSet owned by a Deployment gets its count set back by it.
- A StatefulSet or DaemonSet with the `OnDelete` strategy restarts its
  pods only when they are deleted.

## Keys

- `Enter` on an action row starts it.
- `Enter` in the value dialog or the pick list goes to the confirmation.
- `Esc` cancels any step; nothing is changed until you confirm.

## Limits

- A paused Deployment cannot be restarted or rolled back; run
  `kubectl rollout resume` first.
- Read-only mode refuses these actions.
- RBAC is checked by the API server: a missing permission shows as an
  error after you confirm.
- Restart and rollback read from kc's cache, which can be a moment
  behind. The dialog shows what kc read, and that is what runs.

## See also

- [Confirmation dialog](confirm.md)
- [Custom commands](custom-commands.md)
- [Read-only mode](read-only.md)
- [Access](access.md)
