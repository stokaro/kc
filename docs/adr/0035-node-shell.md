# 0035. Node shell

- Status: Accepted
- Date: 2026-11-22
- Issue: #35

## Context

k9s can open a shell on a node by starting a privileged pod there. The
issue asks for the same in kc: a "Shell" action on nodes, a red
confirmation like the podfs helper's (#21), the shell full screen in kc's
terminal, the pod deleted on exit, and the feature off by default behind a
config flag, per context if possible.

Open questions: where the action lives, what the pod looks like, which
image and namespace, how the pod is cleaned up when kc does not get the
chance, and how the shell reaches kc's screen. kc's built-in terminal and
the F9 command panel both run local processes in a PTY (bubbleterm); kc
has no exec-into-pod plumbing yet. #28 (container shell and attach) will
need the same plumbing. PR #80 (not merged) adds an object-action registry
(`internal/objectaction`) and an "Object Actions" section in the F9 menu.

## Decision

### 1. Where the action lives

Entering a node shows `/pods` and, when the node shell is enabled for the
current context, a row `Start a node shell...`. Enter opens the red
dialog. This uses the existing `models.ConfirmedAction` path, as the podfs
"Start a debug helper..." row does, and does not touch the F9 menu that
PR #80 changes.

The row's confirmation and start logic do not depend on the row: the
dialog text comes from `NodeShellItem.Confirmation`, the start from the
`models.NodeShellRequest` message the UI handles. Once the object-action
registry lands, a "Shell..." action for `v1/nodes` can return the same
confirmation and request, and the row can go.

`models.ConfirmFollowUp` is new: a confirmed action may hand the UI a
message after `Confirm` succeeds. The node shell needs it because only the
UI can open a terminal.

### 2. The pod

`internal/nodeshell.NewPod`:

- `nodeName: <node>`: no scheduler, so cordoned or full nodes work.
- `hostPID`, `hostNetwork`, `hostIPC: true`; one container `shell` with
  `privileged: true` and `runAsUser: 0`. This is what `kubectl debug node`
  does with `--profile=sysadmin`.
- `tolerations: [{operator: Exists}]`: every taint, `NoExecute` included.
- `restartPolicy: Never`, `terminationGracePeriodSeconds: 0`,
  `automountServiceAccountToken: false`, `enableServiceLinks: false`.
- `activeDeadlineSeconds` from `nodeShell.maxDuration` (default 8h).
- Labels `app.kubernetes.io/component=kc-node-shell`,
  `app.kubernetes.io/managed-by=kc`; annotation `kc.sttts.github.io/node`.
- Name `kc-node-shell-<node>-<5 random>`, cut to 63 characters. It is
  chosen when the dialog opens, so the dialog names the pod that is
  created.

The container's main process waits for a heartbeat file and exits after
60 seconds without a touch (as the podfs helper does). The shell is an
exec of `nsenter -t 1 -m -u -i -n -p -- sh -c '<bash -l, else sh -l>'`,
so it runs in all namespaces of the host's PID 1 as root.

### 3. Heartbeat from kc, not from the shell

The first design touched the heartbeat from a loop that lived as long as
the shell. A test on a k3s node (containerd 2.3) showed that a shell whose
exec client disconnects keeps running: the runtime does not end an exec
process when its stream breaks. The pod would then live until
`activeDeadlineSeconds`.

kc therefore runs a second exec stream next to the shell,
`while read -t 30 _; do touch <heartbeat>; done`, and writes a newline
every 10 seconds. When kc dies, the writes stop, the loop ends, and the
pod exits about 60 seconds later. Measured on the k3s node: with the
keepalive the pod ran past 80 seconds; after the streams were dropped it
was `Succeeded` 66 seconds later. After that pod was deleted, a later
node shell saw no orphaned host shell.

### 4. Cleanup

- Shell ends (`exit`, `C-d`, `C-]`, broken stream): kc deletes the pod in
  a background command (grace period 0, UID precondition, NotFound is
  fine) and reports the result as a toast.
- The pod does not come up (image pull error, failed, 2 minutes without
  running, kc quitting): `nodeshell.Start` deletes it before it returns.
- kc quits with a shell open: `Run` deletes the pod before it cancels the
  root context.
- kc is killed: the heartbeat stops, the pod exits within about a minute
  and stays as a `Completed` pod without running processes. The labels
  make leftovers easy to find.

### 5. Image, namespace and configuration

```yaml
nodeShell:
  enabled: false        # off by default
  contexts: []          # empty: every context while enabled
  image: docker.io/library/busybox:1.36.1
  namespace: default
  maxDuration: 8h
```

The image needs only `sh`, `stat`, `date` and `nsenter`; busybox has all
of them and the podfs helper already uses the same image, so a node that
pulled one has the other. `namespace: default` follows `kubectl debug
node`. Clusters that enforce Pod Security `baseline` refuse the pod; the
create error ("violates PodSecurity ...") is shown as is, and the README
says to label the namespace `pod-security.kubernetes.io/enforce=privileged`.
kc does not create or label namespaces itself.

"Per context" is a list of context names under the one flag, not a
per-context config section: kc has no per-context configuration yet, and
a list is enough to say "only on my kind clusters".

### 6. The confirmation

Title "Start a privileged node shell?". Details: context, node,
namespace, pod name, image. Notes: that the pod is privileged with host
PID, network and IPC namespaces and tolerates every taint, that the shell
runs as root in the node's namespaces, the cleanup policy, and "Similar
to:" with the matching `kubectl debug node/<node> -n <ns> -it
--profile=sysadmin --image=<image> -- nsenter ... -- sh` (with `--context`
when plain kubectl uses another context). It says "similar", because
kubectl names its pod differently and does not delete it. The command is
wrapped into the notes, since `models.Confirmation` has no command field
yet (PR #80 adds one; the line can move there).

### 7. How the shell is shown

kc execs in-process with client-go (`internal/podexec`: WebSocket, falling
back to SPDY like kubectl) and feeds a bubbleterm emulator through pipes
(`bubbleterm.NewWithPipes`) instead of a local PTY (`internal/ui`
`execView`). The shell owns the whole screen; the last line is a red
status bar naming the node and pod. All keys and pastes go to the shell;
`C-]` (the telnet escape) ends it. Window size changes reach the remote
TTY through a size queue.

The node shell adds no key to the panels. `C-]` is the only new key, and
it is read only while the shell is open; neither main nor the open PRs
use it (checked against the list of taken keys: `M-c`, `M-a`, `M-s`,
`M-o`, `M-r`, `M-y`, `M-u`, `M-H`, `M-f`, `M-p`, `C-\`, `C-z`, `C-s`,
`C-u`, `C-o`, `F1` to `F10`).

`execView` and `podexec.Stream` know nothing about nodes, so #28 can use
them for container shells and attach with a different target.

## Alternatives considered

- **F9 entry on the node row.** Matches "an action on node rows" best, but
  the F9 menu holds `appconfig.CommandConfig` entries only; adding a
  built-in entry now would collide with PR #80's "Object Actions" section.
  It moves there once #80 lands.
- **`kubectl exec -it` in the F9 command panel or the shell line.** Needs
  kubectl, and the command panel does not set `KUBECONFIG` for the current
  context (a TODO in `panel_command.go`), so it could run against the
  wrong cluster or without kc's impersonation. Typing into the user's
  shell cannot tell kc when the shell ended, so the pod could not be
  deleted reliably.
- **`kubectl debug node/<name>` itself.** Same kubectl dependency, and it
  leaves the pod behind.
- **Heartbeat loop inside the shell exec.** Measured not to stop when kc
  dies (section 3).
- **A periodic short exec from kc (`touch`) as heartbeat.** Works, but is
  a new API request every few seconds; one long stream is one request.
- **Only `activeDeadlineSeconds`.** A crashed kc would leave a privileged
  pod running for hours.
- **`C-o` or double `Esc` to leave the shell.** `C-o` is kc's terminal
  toggle and would suggest the shell keeps running in the background; a
  double `Esc` is common in vim. `C-]` is rarely bound in shells.
- **Detach and return to the shell later.** More state (a hidden session,
  an indicator, a way back) for little gain; left for #28 if attach needs
  it.
- **Mounting the host root at `/host` (k9s, `kubectl debug`).** `nsenter`
  into PID 1 gives the real host view (mounts, processes, network,
  hostname) without a hostPath volume.

## Consequences

- The node shell is off until a user enables it, per context if wanted.
- A shell needs `create` and `delete` on pods and `create` on `pods/exec`
  in `nodeShell.namespace`, and a namespace that allows privileged pods.
- Hosts without `sh` (Talos, Bottlerocket) cannot run the shell; the exec
  fails and the pod is deleted.
- While the shell is open, panels do not update on screen; they catch up
  when it closes.
- Follow-ups: move the action to the F9 object-action registry after
  PR #80; use `execView`/`podexec` for #28; optional resource requests and
  a priority class for the pod if clusters with quotas need them.
