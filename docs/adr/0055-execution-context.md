# 0055. One execution context for terminal commands

- Status: Accepted
- Date: 2026-11-22
- Issue: #55

## Context

kc starts three kinds of subprocesses: the integrated shell, `F4`
(`kubectl edit`) and custom commands from the `F9` menu (the default
`Top Nodes`, `Shell` and `Top Pods` are custom commands too). They disagreed
on which cluster and identity they used:

- custom commands inherited kc's environment, so they used the kubeconfig's
  current context, not the one kc showed;
- a context switch deleted the shell's generated kubeconfig and made a new
  one at another path, while the running shell kept the old path;
- `F4` did not pass `--as`, `--as-uid` or `--as-group`;
- the overlay kubeconfig dropped impersonation when the namespace was empty
  (kc's "all namespaces"), and otherwise wrote a user entry with only the
  impersonation fields. Kubeconfig merging takes a named entry whole from
  the first file, so that entry hid the user's token, certificate or exec
  plugin.

The issue left open how impersonation reaches subprocesses, what the single
context type looks like, and how the shell follows context changes without
editing the user's kubeconfig.

## Decision

1. **One type.** `internal/execctx.Context` holds the kubeconfig file, the
   loaded config, the context name, the namespace and the impersonation
   (user, UID, groups). The app builds it in one place
   (`App.execContext`) from kc's selected context, the namespace of the
   panel the action comes from, and the `--as`, `--as-uid` and
   `--as-group` flags. An empty namespace keeps the namespace the
   kubeconfig context sets; kc uses empty for "all namespaces", and
   kubeconfig cannot express "all namespaces".

2. **Flags where kc builds the argv, a generated kubeconfig where it does
   not.** `F4` runs `kubectl edit <ref> --kubeconfig <file> --context <ctx>
   --namespace <ns> --as ... --as-uid ... --as-group ...` (one
   `--as-group` per group). kubectl applies these flags on top of the
   user entry, so the user's credentials stay and no credential is
   copied. The shell and custom commands are arbitrary programs (helm, k9s,
   scripts), so flags cannot be injected; they get `KUBECONFIG` pointing at
   a kubeconfig kc generates.

3. **Generated kubeconfigs** live in a private directory:
   `<tmp>/kc-<uid>/<pid>-*`, mode 0700, files mode 0600, written by
   atomic rename. kc checks that `kc-<uid>` is a directory owned by the
   user (not a symlink) and tightens it to 0700; otherwise it uses a fresh
   `<tmp>/kc-*`. The directory is removed on exit, and directories of
   dead kc processes are removed at start. The user's files are only
   read; tests compare them byte for byte before and after.

   - `terminal.mode: overlay` (default): `KUBECONFIG=<slot file>:<user's
     files>`, where the user's files are what plain kubectl reads
     (`KUBECONFIG`, else `~/.kube/config`). The slot file holds the
     current context and the context entry with the namespace. It also
     holds the user entry, with all its credentials plus the
     impersonation fields, when kc impersonates; and the cluster or user
     entry when an earlier file in the chain has one of the same name, or
     when the context comes from a file outside the chain. Relative file
     references in copied entries are made absolute.
   - `terminal.mode: copy`: `KUBECONFIG=<slot file>`, a full copy of the
     file that defines the context, with the current context, namespace
     and impersonation set.

4. **The shell follows context changes by rewriting, not re-pointing.**
   The shell has one slot whose `KUBECONFIG` value never changes for the
   life of kc. A context switch or a namespace change of the active panel
   rewrites the slot, and the shell sees it with its next command. Before,
   the value contained the base file path, so a switch to a context in
   another file changed it; now it does not. `terminal.follow: false`
   leaves the shell's environment untouched, as before.

5. **Custom commands** get a slot per command widget (`cmd-<n>`), rewritten
   on each start and restart (watch interval, debounce) and removed when
   the widget is torn down. The context is read at each start, so a
   watching command moves to the new context on its next run after a
   switch. The namespace is the selected item's, else the panel's.
   `.Context`, `.As`, `.AsUID` and `.AsGroups` are added to the template
   data for tools that need flags. Templates without items still run
   verbatim, so a global command with literal `{{` (a kubectl
   go-template) keeps working.

## Alternatives considered

- **Flags everywhere** (`--context`, `--as`): cannot reach programs kc does
  not build the argv for, and kubectl has no environment variable for
  `--as`. Rejected for the shell and custom commands.
- **A generated kubeconfig for `F4` too**: works, but copies credentials
  into a file when kubectl can take flags. Flags also make the command kc
  prints copy-pasteable. Rejected.
- **Overlay with only impersonation fields in the user entry** (the old
  behavior): hides the credentials, see Context. Rejected.
- **Overlay that never copies the user entry, using a new user name**: a
  kubeconfig user entry cannot refer to another one, so the credentials
  have to be copied either way. Rejected.
- **A symlink to the selected kubeconfig behind a stable path**: client-go
  resolves relative paths from the symlink's directory, which breaks
  `client-certificate: certs/x.crt` and similar. Rejected.
- **Restarting the shell on a context switch**: loses the user's shell
  state and running programs. Rejected.
- **Exporting `KUBECONFIG` into the running shell by typing into the PTY**:
  intrusive, breaks when a program other than the shell is in the
  foreground. Rejected.

## Consequences

- With impersonation, or with shadowed names in overlay mode, the user's
  credentials of the selected context are copied into a 0600 file in a 0700
  directory for the life of kc. Copy mode already did this for the whole
  file.
- Credentials come from the kubeconfig kc loaded at start. An auth provider
  that writes refreshed tokens back (the legacy `auth-provider` stanza)
  writes them into kc's file, and kc overwrites them on its next rewrite.
  Exec plugins and static credentials are not affected.
- A custom command that is already running (an interactive `$SHELL` in a
  panel, a long `kubectl logs -f`) keeps the context it started with until
  it restarts. Only the integrated shell follows a switch while running.
- `terminal.mode` is read when kc first generates a kubeconfig; a later
  config change takes effect at the next start.
- The kubectl commands kc shows above the shell line (`M-c`) add
  `--context` but not the impersonation flags yet; that is left for a
  follow-up.
- The old `internal/ui/termctx` package and its unused external-change
  detection are removed.
