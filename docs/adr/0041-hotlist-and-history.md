# 0041. Directory hotlist and navigation history

- Status: Accepted; a jump to another context moving both panels is superseded by [0362](0362-independent-panel-contexts.md)
- Date: 2026-11-22
- Issue: #41

## Context

Esc and Backspace only pop a panel's navigation stack. Users cannot return to
a place they visited earlier or save places they use often. mc has both: a
directory hotlist on `C-\` and a per-panel directory history on `M-y`, `M-u`
and `M-H`. The issue proposes the same keys and storing the hotlist in
`~/.kc/config.yaml`.

Open questions:

1. Which keys, given the mc notation kc uses.
2. What a saved location is. Folder paths such as
   `/namespaces/<ns>/<group>/<version>/<resource>` are labels; the panel
   reaches a folder by entering rows, and row IDs differ from the labels.
   AGENTS.md requires GVR-based identity, never a bare resource name.
3. Where the hotlist is stored. `appconfig.Save` rewrites the whole config,
   and PR #81 (not merged) adds `appconfig.Store.Update` for safe config
   writes.
4. How the history behaves: truncation after going back, duplicates, size.
5. How to reach a saved place when the folder rows are still loading. The
   startup intent has the same problem (#84): it looks for the type row
   before the root has listed it.

## Decision

### 1. Keys

- `C-\` opens the hotlist dialog. In it, `Enter` goes to the highlighted
  entry, `Ins` adds the active panel's location, `Del` or `F8` removes the
  highlighted entry, typing filters, `Esc` closes.
- `M-y` goes back and `M-u` goes forward in the active panel's history.
- `M-H` lists the history, newest first, the current entry marked.
- `Esc y`, `Esc u` and `Esc H` are the fallbacks where Option does not send
  Meta, like the other `Esc <key>` fallbacks.
- The keys work in panel mode only. With the full-screen terminal shown
  (`C-o`), they go to the shell, where `M-y`, `M-u` and `C-\` have their own
  meaning.

### 2. Location

A location (`navigation.Location`) is:

- `context`: the kubeconfig context name;
- `namespace`: the panel's namespace scope (`""` for all namespaces);
- `steps`: the row IDs entered from the panel root, as the navigator
  stored them (`Navigator.Steps`). The first step of a resource list is the
  resource row ID, `<ns>/<group>/<version>/<resource>` or
  `<group>/<version>/<resource>`, so a location is keyed by GVR; deeper
  steps are object names and sub-resource row IDs;
- `path`: the breadcrumb, shown in the dialogs and ignored for equality.

Going to a location replays the steps with `navigation.GoTo`. A location in
another context switches kc to that context first (as the context picker
does, which puts both panels at their roots), then walks the steps. A
location with another namespace scope rescopes the panel. When the target
shares a prefix with where the panel is, the panel goes back to the common
prefix and enters only the rest, so `M-y` to a parent is a plain back and
the row the panel came from stays highlighted.

### 3. Storage

The hotlist is a state file of its own, `~/.kc/hotlist.yaml`:

```yaml
entries:
- context: mba
  namespace: default
  path: /pods
  steps:
  - default//v1/pods
```

kc writes it after every add or remove: a temporary file in `~/.kc`, mode
0600, renamed over the old file. The config file is not touched. A file
that fails to parse is reported at startup and left alone until the user
changes the hotlist.

The history is kept in memory only, per panel, and is lost on exit, as in
a fresh mc session.

### 4. History rules

- Every panel move is recorded: entering or leaving a folder, a scope
  change, a context switch, a hotlist jump, the startup intent.
- Recording after going back drops the entries ahead of the cursor, like a
  browser.
- A location is kept once: visiting it again moves it to the end.
- At most 50 entries per panel; the oldest is dropped.
- `M-y`, `M-u` and choosing from the `M-H` list move the cursor and do not
  record, so the forward entries stay.
- `C-u` (swap panels) swaps the histories along with the panels.

### 5. Waiting for rows

`navigation.GoTo` returns `*navigation.ItemNotFoundError` for a missing
row. A jump treats it as "not loaded yet": it keeps the folders it already
entered and retries the rest every 250 ms, for up to 40 attempts (10 s).
Each attempt runs on the update goroutine with the short panel timeout; the
pause between attempts runs off it and asks the waiting folder for its rows,
which starts and waits for its informer. A jump stops quietly when the user
moves the panel, and with a red message when the attempts run out.

## Alternatives considered

- Store the hotlist in `~/.kc/config.yaml` under a `hotlist:` key, as the
  issue proposes. Rejected: `appconfig.Save` writes the whole config, so a
  hotlist change would rewrite settings the user edits by hand, and a
  dialog that saves defaults (`C-s` in options) would write the hotlist.
  Until `Store.Update` (#81) lands, both writers would race on one file.
  The hotlist also changes far more often than settings. A separate file
  needs no config schema change and no sync between `Default()`,
  `config-default.yaml` and the README.
- Store locations as breadcrumb paths (`/pods/web`). Rejected: labels are
  not row IDs, they drop the API group and version, and they are ambiguous
  for types with the same name in different groups.
- Store the GVR, namespace and object name as typed fields. Rejected for
  now: it covers resource lists and objects but not sub-resource folders
  (containers, logs, config map keys). Row IDs cover all of them and are
  already GVR-based where it matters.
- Browser-style history without de-duplication. Rejected: moving between
  two lists fills the history and the `M-H` list with repeats. mc also
  keeps each directory once.
- Record history moves too, as mc does when a directory is chosen from the
  `M-H` list. Rejected: it would drop the forward entries on every `M-y`.
- Persist the history. Not needed for the issue; can be added to the state
  file later.
- Fix #84 here by moving the startup intent onto the waiting jump.
  Not done: the startup intent continues synchronously (select rows, open
  the manifest preview), which needs a continuation on the jump. The jump
  code (`jumpTo`, `continueJump`) is the piece #84 can reuse.

## Consequences

- Hotlist entries name contexts and namespaces; the file is 0600.
- An entry whose objects are gone stops at the deepest folder that still
  exists, with a message naming the path.
- Entries match contexts by name. With the same context name in two
  kubeconfig files, the first one kc discovered wins.
- Going to an entry in another context also moves the other panel to its
  root, because kc has one context for both panels.
- Entries cannot be renamed or reordered yet; the label is the breadcrumb.
  mc's "Add current" shortcut outside the dialog (`C-x h`) is not bound.
