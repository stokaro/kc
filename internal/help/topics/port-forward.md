# Port forward

`M-p` forwards a local port to a pod, a container of a pod, or a
service, as `kubectl port-forward` does. Use it to reach a web UI, a
metrics endpoint or a database inside the cluster from your machine.
kc runs the forward itself, so it needs no kubectl and no extra
terminal.

## The dialog

The dialog has one field, `Local:remote`, in kubectl's syntax:

- `8080:80`: local port 8080 to port 80 in the pod.
- `8080`: the same port on both sides.
- `:80`: any free local port to port 80. kc shows the port it got.
- `8080:http` or `http`: a port by its name.

The field starts with the first declared TCP port on both sides. Below
it the dialog lists the declared ports, the local address it listens
on, and the equivalent `kubectl port-forward` command, which follows
what you type.

For a service, kc picks a running pod behind it, ready pods first, and
maps the service port to its target port, as kubectl does. The forward
stays on that pod.

## The /port-forwards folder

While forwards run, the panel root shows `/port-forwards` with their
count. It lists every forward of this kc process, across contexts, with
the local address, target, pod, namespace, context and age.

- Forwards keep running when you switch context or namespace.
- A forward ends with a red message when its pod goes away or the
  connection is lost. kc does not reconnect.
- All forwards stop when kc exits.

## Keys

- `M-p` or `Esc p` on a pod, container or service row opens the dialog.
- `Enter` starts the forward; `Esc` cancels.
- In `/port-forwards`: `F3` shows the details and the kubectl command,
  `F8` stops the forward after a confirmation.

## Limits

- One port pair per forward, TCP only.
- `M-p` works in list mode only; other rows say "Port forward works on
  pods, containers and services".
- kc listens on `127.0.0.1` unless `portForward.address` in
  `~/.kc/config.yaml` says otherwise. `0.0.0.0` exposes the port to
  your network.
- A service without a selector cannot be forwarded.
- Read-only mode refuses new forwards. Stopping one with `F8` stays
  allowed: the forward runs in kc, so the cluster does not change.
- You need create on `pods/portforward` in the namespace.

## See also

- [Delete](delete.md)
- [Read-only mode](read-only.md)
- [Access](access.md)
