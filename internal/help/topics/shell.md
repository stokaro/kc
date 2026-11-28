# Shell and terminal

kc runs your shell below the panels, as mc does. The shell line under
the panels takes what you type; `C-o` shows the same shell full screen.
From a container or pod row, `M-x` opens a shell inside the container
and `M-t` attaches to its main process.

## The shell line and C-o

- Letters, digits and `Space` go to the shell line; `Enter` runs the
  line when it has text.
- Once you typed, the shell line gets every key, so the shell's line
  editing works: `Tab`, `Left`, `Right`, `Home`, `End`, `C-u`, `C-w`,
  `C-r`, `Esc` and the Meta keys. kc keeps only the function keys, `C-o`,
  `C-1`, `C-2`, and `Up`, `Down`, `PgUp` and `PgDn`, which move the
  panel cursor. `Enter` runs the line and `C-c` cancels it; then the
  keys belong to the panels again. Erasing the line gives them back too,
  unless a key such as `Tab` or `M-b` changed it in a way kc cannot
  follow. Spaces alone do not count as text, nor do keys that type
  nothing on an empty line, such as `C-l` or `C-e`.
- Use `Esc` plus a digit or a letter for a function or Meta key while
  the shell line is empty; with text on it, `Esc` goes to the shell.
- `C-o` switches to the full-screen terminal and back. There every key
  goes to the shell at once, `Esc`, `Tab`, `F9` and `F10` included; only
  `C-o` or a click on the bottom line returns to the panels. Text typed
  under the panels is still on the line there; erase it or press `C-c`,
  and the panels get their keys back when you return. What you type in
  `C-o` after `Enter` may go to another program, so it does not count.
- The shell runs with `KUBECONFIG` set to a file kc generates, so
  `kubectl` talks to the context, namespace and identity of the active
  panel. A Describe, Manifest, Logs or Info panel gives the context of
  the panel it follows, or of its pin. The shell follows a change of the
  active panel, its context or its namespace from its next command on;
  a program already running keeps what it started with.
  `terminal.follow: false` leaves the shell's environment alone.

## cd on the shell line

As in mc, `cd` typed on the shell line moves the active panel instead
of running in the shell:

- `cd pods`, `cd po`, `cd deploy/myapp`: a type (any name kubectl
  accepts) and optionally an object.
- `cd /namespaces/kube-system/pods`: an absolute path, as the panel
  title shows it. `cd ..` goes up; `cd ./name` stays relative.
- `cd -n kube-system svc`, `cd -A pods`: change the scope first.
- `cd -` returns to the place before the last jump; `cd` or `cd ~` goes
  to the root in the namespace kc started in.

kc waits up to 10 seconds for each part of the path to appear. Only
plain `cd` lines are taken over: after `Tab`, a cursor key, or with
shell syntax such as `&&` or `$`, the line goes to the shell.
`builtin cd` always reaches the shell.

## Shell and attach in a container

- `M-x` (or `Esc x`) runs `kubectl exec -it` with `bash`, or `sh` when
  the image has no bash. `Enter` on a container's `shell` row does the
  same.
- `M-t` (or `Esc t`) runs `kubectl attach`, with `-i` and `-t` when the
  container has stdin and a TTY. Keys go to the main process, so ending
  it ends the container.
- On a pod row kc picks the container named by the
  `kubectl.kubernetes.io/default-container` annotation, else the first
  one. Open the pod's `/containers` folder to pick another.
- kc prints the kubectl command, hands the terminal to kubectl and
  comes back when the session ends. `C-c` ends kubectl, not kc.

## Node shell

With `nodeShell.enabled: true` (optionally only for the contexts in
`nodeShell.contexts`), a node shows a `Start a node shell...` row.
`Enter` opens a red confirmation that names the context, node, pod and
image. kc then creates a privileged pod on that node and opens a root
shell in the host namespaces, full screen. `exit` or `C-]` ends it, and
kc deletes the pod.

## Limits

- Shell and attach need `kubectl` in `PATH`. When the image has no
  shell, kc says so; the `/root` row can still browse the files.
- The node shell needs a namespace whose pod security allows privileged
  pods, and create and delete on pods and `pods/exec`.
- Read-only mode refuses `M-x`, `M-t` and the node shell, but not the
  integrated terminal.
- The `sh` kc keeps open to browse a container's files is separate from
  `M-x`. It closes after 60 seconds without use and opens again on the
  next listing.

## See also

- [Container files](container-fs.md)
- [Read-only mode](read-only.md)
- [Context and namespace](context-namespace.md)
- [Custom commands](custom-commands.md)
- [Panels](panels.md)
