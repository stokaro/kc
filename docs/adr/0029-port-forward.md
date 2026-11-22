# 0029. Port forwarding for pods, containers and services

- Status: Accepted
- Date: 2026-11-22
- Issue: #29

## Context

kc had no port forwarding. k9s opens a dialog with Shift-F, lists active
forwards under `:pf`, and reads defaults from `k9scli.io/port-forwards`
annotations. The issue asks for a dialog on pod, service and container rows
with ports prefilled from the spec, an in-process forward through client-go,
and a `/port-forwards` folder where forwards are listed and stopped. It
leaves open whether forwards survive a context switch, whether they bind to
localhost only, and whether the k9s annotations are supported.

Constraints from the project: kc imitates Midnight Commander, so plain
letters go to the shell line and shortcuts use Meta (`M-c`, `M-s`) with an
`Esc <letter>` fallback; API work must not run on the Bubble Tea update
goroutine; reads go through the shared controller-runtime cache; every real
client request logs an INFO line.

## Decision

1. Key and entry point. `M-p` opens the dialog on the selected pod row,
   container row (under a pod) or service row; `Esc p` does the same where
   Option does not send Meta. It works only in list mode. On any other row
   kc shows "Port forward works on pods, containers and services". P
   stands for "port". The first version used `M-f` after k9s' Shift-F;
   it moved to `M-p` because `M-f` is the row filter (#30), the mc-style
   "filter" key.

2. Dialog shape. One text field, `local:remote`, in kubectl's syntax:
   `8080:80`, `8080` (same port both sides), `:80` (any free local port),
   `8080:http` or `http` (port names). Below the field the dialog lists the
   declared TCP ports (`80/http (nginx)`) and the equivalent
   `kubectl port-forward` command, updated as the user types. One field
   instead of two keeps the syntax users already know and lets them paste a
   pair. Only one pair per forward.

3. Local port choice. The field is prefilled with the first declared TCP
   port on both sides (`8080:8080`, for a service its service port), as
   kubectl does for a bare port. `:remote` asks the system for a free port;
   the bound port is shown in the confirmation and in `/port-forwards`.
   Ports below 1024 are not remapped; if the bind fails, the error is shown
   and the dialog value can be changed on the next attempt.

4. Remote side, as kubectl resolves it. For a pod, a number is used as is
   and a name is looked up in the container ports; the pod must be Running.
   For a service, kc lists the pods in the namespace from the shared cache,
   filters by the service selector, sorts them with kubectl's
   `podutils.ActivePods` (ready, running, scheduled first), takes the first
   running pod that is not deleting, and maps the service port to its target
   port with kubectl's `LookupContainerPortNumberByServicePort` (named target
   ports, headless services). A service without a selector is refused. The
   forward stays on the chosen pod, like `kubectl port-forward svc/...`.

5. Transport. In-process with client-go `tools/portforward`: SPDY tunneled
   over a websocket first, plain SPDY when the upgrade fails or an HTTPS
   proxy refuses it, the same fallback kubectl uses by default. The POST to
   `pods/<name>/portforward` is logged at INFO before it is issued.
   Per-connection errors from client-go go to the log, not the terminal.

6. Bind address. `127.0.0.1` by default, configurable as
   `portForward.address` in `~/.kc/config.yaml` (`localhost` binds both
   loopback addresses like kubectl; `0.0.0.0` exposes the port to the
   network). The shown kubectl command adds `--address` unless the address
   is `localhost`, so it matches what kc does.

7. Lifetime. Forwards belong to the kc process, not to a panel or context:
   they keep running across context and namespace switches and panel
   navigation. A forward that ends on its own (pod deleted, connection lost)
   is removed from the list and reported as a red message; kc does not
   reconnect. All forwards stop when kc exits (bounded wait of 2 seconds).

8. Where forwards are listed and stopped. While at least one forward runs,
   each panel root shows a `/port-forwards` row with the count. The folder
   lists every forward with local address, target, pod and pod port,
   namespace, context and age. `F3` shows the details and the kubectl
   command; `F8` asks for confirmation and stops the forward. Stopping does
   not change the cluster, but F8 confirms in kc, so it confirms here too.

9. k9s annotations (`k9scli.io/port-forwards`) are not supported.

## Alternatives considered

- Shelling out to `kubectl port-forward`: needs kubectl on PATH, gives no
  bound-port feedback without parsing output, and leaves processes to reap.
  The issue asks for in-process.
- Separate local and remote fields: two inputs to tab between, and no way to
  paste kubectl's pair syntax.
- Prefilling a remapped local port (80 becomes 8080): guesses, and differs
  from kubectl. The system-chosen port (`:80`) covers the privileged case.
- Showing the count in the panel frame: the frame already carries the
  context, namespace and scroll state; a root row with a count is visible
  where the folder is entered and needs no frame changes.
- Always showing `/port-forwards` at the root: adds a row every user sees on
  every root, mostly empty.
- Stopping forwards on context switch: surprising when a forward to one
  cluster is in use while browsing another; the context column makes the
  owner clear.
- Following the service to another pod when the first one dies: kubectl
  does not do it either, and it would hide that the backing pod changed.

## Consequences

- New package `internal/portforward` (port parsing and resolution, the
  registry, the client-go runner); `models.Deps` carries the registry.
- The `/port-forwards` row appears and disappears at the root as forwards
  start and stop; the root and the folder subscribe to the registry only
  while a panel shows them.
- Only one port pair per forward and TCP only. Multiple pairs, the k9s
  annotations, automatic reconnect, and forwards for Deployments or other
  workloads (resolve to a pod like kubectl) are possible follow-ups.
- A forward to a namespace-scoped or impersonated context uses the rest
  config of the folder it was started from, so it carries the same
  identity as the panel.
