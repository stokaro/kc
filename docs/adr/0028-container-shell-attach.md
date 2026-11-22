# 0028. Shell and attach for pod containers

- Status: Accepted
- Date: 2026-11-22
- Issue: #28

## Context

k9s opens a shell in a container with `s` and attaches with `a`. kc had no built-in exec; users wrote an F9 custom command or typed `kubectl exec -it` on the shell line. The issue proposed a `/shell` row per container, Shell and Attach on pod rows with a container choice, and left open whether to reuse the podfs debug helper for images without a shell and whether the shell should be configurable.

Constraints at the time of writing:

- PR #96 (node shell, #35) adds `internal/podexec`, an in-process client-go exec stream with stdin, TTY and resize, and `internal/ui/exec_view.go`, a full-screen terminal fed by pipes. It is not merged, and this change must not copy that code.
- PR #102 adds access checks (`create` on `pods/exec` and `pods/attach`), and the read-only work of #42 is in progress. Neither is merged.
- PR #88 makes the shell line, F4 and custom commands run on the selected panel context. Not merged either.
- Plain letter keys go to the shell line in kc, as in mc, so `s` and `a` are not available.

## Decision

### 1. Run kubectl with the terminal handed over

A session runs `kubectl exec -it` or `kubectl attach` through `tea.Exec`, the way F4 runs `kubectl edit`: Bubble Tea releases the terminal, kubectl owns it until the session ends, and kc redraws. kubectl handles raw mode, window resize and the stream protocol. kc prints the equivalent kubectl command (`[kc] kubectl ...`) on the released screen before kubectl starts.

The kubectl arguments name the panel's identity explicitly, taken from the panel folder's cluster key: `--kubeconfig` (when the key has a path), `--context`, and `--as`, `--as-group` and `--as-uid` when kc impersonates. So the session talks to the cluster the panel shows whatever kubectl's own current context is, and does not depend on #88. The shown command drops `--kubeconfig`, and drops `--context` when plain kubectl already uses that context, like the other commands kc shows; impersonation flags stay, since they decide what the session may do.

kc keeps the last 4 KiB of kubectl's stderr (tee'd, the terminal still shows it) to explain a failure in a toast after it takes the screen back.

### 2. Keys

- `M-x` (fallback `Esc x`): shell in the selected container or pod.
- `M-t` (fallback `Esc t`): attach to the selected container or pod.
- Enter on the new `shell` row of a container folder (next to `logs` and `/root`): shell.

`M-x` reads as "exec", `M-t` as "tty". Both were free on main and in the open PRs (checked with `git grep '"alt+'` and the diffs of all open PRs). Rows that are neither pods nor containers ignore both keys.

### 3. Container choice on a pod row

A pod row opens the container named by the `kubectl.kubernetes.io/default-container` annotation, looked up in containers, init containers and ephemeral containers as kubectl does; when the annotation is missing or names no container, the first container. There is no picker: kubectl behaves the same way, and the pod's `/containers`, `/init-containers` and `/ephemeral-containers` folders list every container with the same keys. When the pod has more than one container, the printed banner names the chosen one and the others.

### 4. Shell fallback

The shell command is `sh -c 'command -v bash >/dev/null 2>&1 && exec bash || exec sh'`, as k9s does. It is not configurable for now.

When the image has no `sh` either, the runtime answers "executable file not found"; kc then shows "`<pod>/<container>` has no shell (tried bash and sh). Browse its files with the /root row, which can use the podfs debug helper, or run kubectl debug." kc does not start the helper from the shell key: the helper creates an ephemeral container, which is a mutation with its own consent flow on the `/root` row.

A shell that ends with a non-zero status ("command terminated with exit code N") is not reported; that status belongs to the user's last command.

### 5. Attach flags

Attach adds `-i` only when the container has `stdin: true` and `-t` only when it has `tty: true`, so kubectl does not warn about a TTY the container never allocated. Without stdin the session only streams output and `C-c` leaves. The banner warns that keys go to the main process, since ending it ends the container; kubectl attach has no detach key.

### 6. One gate for permissions and read-only mode

`App.execGate` in `internal/ui/container_exec.go` is called before every session and is the single place where #42 (read-only) and #102 (`create` on `pods/exec` / `pods/attach`) plug in. Today it allows every session; kubectl reports a denial itself, and kc shows kubectl's message.

## Alternatives considered

- **In-process exec through `internal/podexec` and `exec_view.go` (#96).** The better end state: `C-o`-style switching between the session and the panels, no kubectl dependency, structured errors. Not merged yet, and copying it would duplicate code under review. Follow-up below.
- **Typing `kubectl exec -it ...` into the integrated terminal (`C-o`).** Works today, but the integrated shell may hold half-typed input or run another program, its `KUBECONFIG` follows the panel only with `terminal.follow` on, and kc cannot tell when the session ended or why it failed.
- **The interactive command panel (`panel_command.go`).** It runs a command inside one panel's rectangle through bubbleterm. A shell wants the full screen, and the panel's double-Esc leave key collides with programs that use Esc (vi, readline).
- **A container picker on pod rows.** More keystrokes for the common single-container pod and a dialog to maintain; the annotation plus the container folders cover the rest.
- **`s` / `a` as in k9s.** Plain letters belong to the shell line.
- **Starting the podfs debug helper when there is no shell.** Creates an ephemeral container (a pod mutation) from a key that otherwise only reads; the `/root` row already offers it with consent.

## Consequences

- Shell and attach need `kubectl` in `PATH`; kc says so when it is missing.
- While a session runs, kc's screen is suspended; `C-o` does not switch back to the panels until the session ends.
- The `--kubeconfig` path is the file kc loaded the context from. A context whose cluster or user entries come from another file of a multi-file `KUBECONFIG` behaves as it does for F4 today.
- Follow-up: switch to `internal/podexec` and `exec_view.go` once #96 lands, keeping the keys, the container choice and `execGate`.
- Follow-up: call the #102 access check and the #42 read-only switch from `execGate`.
- Follow-up: a configurable shell command per container or image, if users ask for it.
