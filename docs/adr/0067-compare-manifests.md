# 0067. Compare manifests selected in the two panels

- Status: Accepted
- Date: 2026-11-22
- Issue: #67

## Context

Two objects can look alike in their lists and still differ in images,
resources, environment or configuration. The two-panel layout should make the
comparison direct: one object in each panel, one key, a readable diff.

The issue leaves several questions open: which key opens the comparison, how
the diff is computed, which fields the default view leaves out, whether the
view is unified or side by side, how refresh and failures behave, and how the
action is reached while kc has no menu bar (#48). Constraints: the read and
the diff must not block the Bubble Tea update goroutine, the manifests come
from the shared cache, the viewer reuses `internal/ui/viewer`, and the
Describe/Manifest panel widgets and `viewer.Widget.SetContent` are being
reworked in other pull requests (#78, #83), so this change must not touch
them.

## Decision

### 1. Key: `C-x C-d`

`C-x C-d` compares the objects under the cursors of the left and the right
panel. It is mc's "Compare files" key. kc gets a small C-x prefix: after
`C-x` the next key completes the command or, like in mc, is dropped. While
the shell line holds typed text, `C-x` stays with the shell. When a panel has
no object under its cursor, a toast says so and nothing opens.

Neither `C-x` nor `C-d` is bound on the panels on main or in the open pull
requests (checked 2026-11-22; `C-d` exists only inside the resource selector
dialog, which takes its keys before the panels). #90 mentions mc's `C-x h`
without binding it; further `C-x` commands should go through the same prefix
handler (`handleCtrlXKey` in `internal/ui/app_compare.go`).

### 2. Diff: a minimal Myers implementation in `internal/textdiff`

`go.mod` has no direct diff dependency. `github.com/pmezard/go-difflib` is an
indirect dependency, but it is archived and implements Python's difflib
(not a shortest edit script). `internal/textdiff` implements the
linear-space Myers algorithm in about 300 lines, renders `diff -u` output
with three context lines, and reports the row of each hunk header so the
viewer can jump between changes. Past an edit distance of 2048 per split it
stops searching for the shortest script and splits at the furthest point
reached; the script stays correct, and large, mostly different manifests
still render in well under a second.

### 3. Views: configuration (default) and raw

`internal/manifestdiff` turns both objects into YAML (keys sorted, lists in
their original order) and diffs them.

- Configuration view (default) omits the fields the API server writes:
  `metadata.uid`, `metadata.resourceVersion`, `metadata.generation`,
  `metadata.creationTimestamp`, `metadata.managedFields`,
  `metadata.selfLink`, and `status`. `s` toggles `status` back in.
- Raw view (`r`) compares the objects exactly as read, `managedFields`
  included.

Nothing else is removed or reordered: annotations such as
`kubectl.kubernetes.io/last-applied-configuration` stay, and the order of
containers, env vars and ports is part of the comparison. The header lists
the view and the fields actually omitted from either side.

### 4. Unified diff in a full-screen viewer

The viewer is a new `DiffViewer` that composes `viewer.Widget` with the
`diff` syntax lexer, so search, scrolling, wrap mode, themes, mouse selection
and copying (`M-a` copies the whole diff, `M-c` the command) work as in the
other viewers. The header names both objects by context, full GVR
(`group/version/resource`), namespace and name; the `---` and `+++` lines
carry the same identity as a path. Keys follow mc's diff viewer: `n`,
`Space` and `Enter` go to the next change, `p` and `Backspace` to the
previous one. Because `n` is taken, search continues with `Shift-F7` (and
`N` backwards). The footer shows `change i/n`. The last line shows the
equivalent shell command,
`diff -u <(kubectl get ... -o yaml) <(kubectl get ... -o yaml)`, with
`--show-managed-fields` in the raw view.

Side-by-side rendering is left for later; the issue allows that.

### 5. Data flow: stable snapshot, explicit refresh

Object rows implement a new `models.ManifestSource` (`ContextName`,
`Manifest(ctx)`), which reads through the shared controller-runtime cache
(`Cluster.GetByGVR`). The viewer opens at once with a loading line; a
`tea.Cmd` reads both objects (10 s timeout) and computes the diff. The read
objects are kept as a snapshot. Switching views re-renders the snapshot in a
`tea.Cmd`; only `C-r` reads the objects again. A failed refresh keeps the
previous diff and shows the error on the command line. Results carry a
sequence number, so a late answer to an older request is dropped. The result
message passes open dialogs, so it is not lost when the viewer options dialog
is on top.

### 6. Contexts

Each object is read through its own row's cluster, and the header shows each
context. Comparing objects from two contexts therefore works when both panels
show different contexts, but it is not a goal of this change and has no
dedicated handling.

## Alternatives considered

- **`go-difflib` or another library.** go-difflib is archived and its
  algorithm is not a shortest diff; `sergi/go-diff` is character-oriented and
  would add a dependency for one feature. Shelling out to `diff` fails on
  systems without it and needs temporary files.
- **Entry in the F9 menu.** F9 opens the custom commands menu built from
  `commands:` in the config; a built-in action there would mix two concepts.
  The mc pull-down menu (#48) is the right home; the action can be added to
  its Command menu when that lands.
- **A different next-change key that keeps `n` for search.** Every other
  choice (`]`, `C-n`) would not match mc. Search continuation is still on
  `Shift-F7`, as in mc.
- **Stripping `last-applied-configuration` or defaulted fields.** They are
  application data or depend on the API server version; hiding them would
  hide real differences. The issue asks not to remove application fields.
- **Re-reading on every informer change.** The diff would move while being
  read; the issue asks for stable snapshots and explicit refresh.
- **Reusing the F3 viewer content path (`ViewContent`).** It returns YAML
  text and runs on the UI goroutine; comparing needs the structured object to
  strip fields, and must run in a command. The new reader uses the same
  shared cache.
- **Side-by-side view now.** Needs column alignment and wide terminals;
  unified diff covers the acceptance criteria.

## Consequences

- One more prefix key (`C-x`) exists; it is free for further two-key
  commands from mc.
- Export to a file is not implemented: `M-a` copies the diff and the command
  line reproduces it in a shell. A save action (F5-style) can follow.
- The configuration view's field list is fixed in
  `manifestdiff.ServerFields`; making it configurable is a follow-up if
  needed.
- The Manifest and Describe panel modes are unchanged; the compare viewer is
  a separate content path.
