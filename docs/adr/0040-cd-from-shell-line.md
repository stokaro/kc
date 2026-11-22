# 0040. cd on the shell line moves the panel

- Status: Accepted
- Date: 2026-11-22
- Issue: #40

## Context

In mc, `cd <dir>` typed on the command line changes the active panel's
directory; the subshell never runs it. kc has a shell line too (the inline
terminal below the panels), but until now everything typed there went to the
shell. Jumping to a resource existed only at startup (`kc get ...`, see
`docs/designs/cli-intents.md`).

The issue left these questions open:

- which path syntax `cd` accepts and how resource names map to types;
- whether other words (`get`, `logs`) are taken over too, and how a user
  then reaches the real shell command;
- the `aliases:` config map for user short names;
- completion.

Constraints found in the code:

- The shell owns the line editor. kc sends keys to a PTY and does not know
  the line after Tab completion, history recall or cursor moves.
- The panel root hides empty types, and types whose emptiness is not known
  yet (#22). A jump right after startup, or to an empty type, cannot find
  the row in the visible list (this is bug #84 for the startup intents).
- Object lists fill from informers; an object row can appear seconds after
  the folder is entered.
- The hotlist and history work (#41) also needs to go to a stored location.

## Decision

### 1. One navigation routine

`(*App).NavigatePanel(idx, PanelPath, onDone)` in `internal/ui` moves a
panel to a location. cd, the startup intents (#84) and the hotlist and
history (#41) all use it.

- `PanelPath` has an anchor (current folder, current folder without
  shortcut, root, home), an optional rescope (`Rescope`, `Namespace`), the
  elements, and `SelectLast` (put the cursor on the last element instead of
  entering it).
- A `PathElem` is one of: `ID` (an exact row ID), `Resource` (a
  `schema.GroupResource`), or `Name` (typed text, resolved at walk time).
- `(*App).PanelLocation(idx)` returns the panel's current location as a
  `PanelPath` of exact row IDs plus its namespace scope. Storing that value
  and passing it back to `NavigatePanel` returns to the same place. This is
  the API #41 should use; it needs no parsing and no RESTMapper.
- `ParsePanelPath(string)` parses the path syntax below.

The walk takes one element at a time. When an element is not there yet, it
looks again every 250 ms with `tea.Tick`, up to 10 seconds per element; the
update goroutine never blocks. Each folder is shown as soon as the walk
enters it, so the user sees the list fill while the walk waits. When an
element does not appear in time, the error says which element and where:
`ghost not found in /pods after 10s`. A name that is not a resource type, in
a folder that lists types, fails at once: `unknown resource type "bogus"`.
On failure the panel stays where the walk stopped, which is always a valid
folder. A newer `NavigatePanel` call on the same panel, a replaced navigator
(context or scope change), or the user navigating that panel ends the walk
silently.

Folders that list types (the root, `/namespaces/<name>`, cluster resources)
gained `TypeItem(schema.GroupResource)`, which also finds hidden rows. A
type named on purpose is entered even when the list hides it, as in mc,
where `cd` reaches a hidden directory.

### 2. Path syntax

Paths are the ones the panel title shows.

| Form | Meaning |
|------|---------|
| `cd /namespaces/default/pods` | from the root of the panel's scope |
| `cd pods/nginx`, `cd nginx` | from the current folder |
| `cd ./x` | from the current folder, no type shortcut |
| `cd ..`, `cd ../svc` | up one level; `..` at the root stays there |
| `cd -` | where the panel was before its last jump; `cd -` twice toggles |
| `cd`, `cd ~`, `cd ~/pods` | home: the root in the namespace kc started in |
| `cd -n kube-system svc` | switch the panel's scope to `kube-system`, then `svc` from the root |
| `cd -A pods` | all namespaces, then pods |
| `cd -n x` | the root of namespace `x` |

Resolution of an element:

- In a folder that lists types, a name is resolved with the cluster's
  RESTMapper, which accepts plural, singular and short names (`po`,
  `pod`, `pods`) and `resource.group` (`deployments.apps`). The match is by
  group and resource; the version is ignored.
- Elsewhere a name matches a row ID or the first column of a row (without
  the leading `/` of folder rows).
- Type shortcut: a relative path whose first element is a resource type
  starts at the root when the current folder does not list types. So
  `cd svc` works from inside a pod, and `cd ns/default` works from
  anywhere. To match an object whose name is also a type name, write
  `cd ./name`.
- The last element is entered when it can be entered and selected
  otherwise. The startup intents select it (`kc get pods nginx` puts the
  cursor on nginx).

### 3. When kc takes over a line

On Enter, kc takes over the line only when all of this holds:

- the panels are visible (in the fullscreen terminal, `C-o`, `cd` runs in
  the shell as usual);
- kc knows the whole line: since the last Enter or `C-c`, only printable
  characters, `Backspace`, `C-u` and single-line pastes were typed;
- the first word is exactly `cd` and no argument contains shell syntax
  (`; & | < > $ ( ) { } \ ' " * ? [ ] ! # =`, except in
  `--namespace=<name>`).

kc then clears the shell's input with `C-u` and moves the active panel.
Usage errors (`cd -n`, `cd a b`, unknown options) show on the command line
as a red message. Everything else goes to the shell unchanged.

To run the shell's own `cd`, use `builtin cd ...` or `command cd ...`, or
press Tab or a cursor key before Enter.

### 4. Only cd is taken over

`get`, `logs` and other words go to the shell. They are real commands people
run in the shell line; taking them over would surprise those users and needs
an escape for every word.

### 5. Left for later

- The `aliases:` map in the config. The RESTMapper already knows the API
  short names; user aliases need a config schema, defaults kept in sync with
  `config-default.yaml` and the README, and a decision on whether an alias
  can name a path. That is a separate change.
- Completion. Tab belongs to the shell; kc does not complete cd paths.
- kubectl-style label selectors (`cd pods app=x`) and context switches
  (`cd @ctx`) from the k9s comparison.

## Alternatives considered

- Read the line from the terminal screen at Enter (the cells between the
  prompt and the cursor). It would also cover history and Tab completion,
  but the screen lags behind the keys (the echo arrives asynchronously),
  zsh autosuggestions draw text after the cursor, and the prompt is
  unknown. A wrong guess would run a different cd than the one typed.
- Shell integration (a `cd` function that reports to kc through an escape
  sequence). Exact, but it means injecting rc files into bash, zsh and fish,
  and it changes the user's shell setup.
- Walk the path at once with `navigation.GoTo` and retry the whole path.
  This is what the startup intents did; it cannot see hidden types and gives
  no per-element error. A walk with per-element waits also shows progress.
- Wait on a goroutine for the rows. The folders are read on the update
  goroutine; a ticked walk on that goroutine needs no new locking.
- Restore the old location on failure. The partial location is valid and
  often useful (`cd pods/typo` leaves the panel on pods with an error), and
  restoring would close and reopen folders for no gain.

## Consequences

- PR #90 (#41, hotlist and history) was written at the same time and has
  its own waiting jump (`jumpTo` / `continueJump` in
  `internal/ui/path_jump.go`, built on `navigation.GoTo` with
  `ItemNotFoundError`). The two overlap. Both retry every 250 ms, give up
  after 10 seconds, and stop silently when the user moves the panel. They
  differ in three ways. This walk waits 10 seconds per element, #90 waits
  10 seconds for the whole jump. This walk finds hidden type rows through
  `TypeItem`, while `GoTo` only sees visible rows. This walk resolves typed
  names, while #90 replays exact row IDs. `Navigator.Steps()` is the same
  method in both changes. Once both are merged, the follow-up is to keep
  one mechanism: #90's `navui.Location` replayed through `NavigatePanel`
  with `PathElem{ID: ...}` elements, its context switch kept in front, and
  `jumpTo` removed.
- Explicit jumps reach empty and not-yet-counted types.
- cd from history recall or after Tab completion goes to the shell. This is
  a known limit of not owning the line editor.
- `C-u` is how the line is cleared; in a shell where `C-u` does something
  else, the typed text stays on the shell line.
- Resolving a type name may trigger API discovery on the update goroutine
  the first time a name is unknown to the cached RESTMapper; the startup
  intents already had this cost.
