# 0030. Row filter for object lists

- Status: Accepted
- Date: 2026-11-22
- Issue: #30

## Context

kc has mc quick search (`C-s`), which only moves the cursor, and `+`/`-`, which mark rows by glob. Nothing hides rows. k9s filters with `/text` (regex), `/!text` (inverse) and `/-l sel` (label selector). mc has a panel filter in its Left/Right menu ("Filter..."), a shell pattern on file names.

The issue leaves open: the key, the syntax, where the filter runs (client-side on cached rows or server-side as a list option), whether it is kept per folder or per panel, how it shows in the frame, and whether the copied kubectl command gets `-l`. Constraints: lists come from the shared controller-runtime cache (AGENTS.md: no ad-hoc watches), rows change live, and `C-s` quick search must keep working. Open PRs touch `folder_objects.go` (#82 sorting, #85 folder Close), so the change there must stay small.

## Decision

1. Key: `M-f` opens a "Filter" dialog; `Esc f` is the Meta fallback, as `Esc s` is for `M-s`. The key only acts when panels have the keyboard (no terminal, command or typed shell input), so `M-f` still moves a word forward in the shell line while you type.
2. Syntax, one of:
   - `text`: Go regular expression on the object name, case-insensitive (k9s does the same). In lists across namespaces it also matches `namespace/name`, so `^kube-system/` works.
   - `!text`: the inverse.
   - `-l selector`: a label selector in `kubectl -l` syntax (`labels.Parse`). Everything after `-l` is the selector, because selectors may contain spaces (`env in (a, b)`).
   An empty text clears the filter. The dialog validates and shows parse errors in place.
3. Client-side filtering. The label selector is applied to the labels of the cached rows. It is not passed to the cache as a list option.
4. The filter belongs to the folder object (`models.RowFilterable`, implemented by `ObjectsFolder`). It stays while the folder is on the panel's navigation stack (enter an object and come back: still filtered) and goes away with it. A newly entered list and a scope or context change start unfiltered.
5. Filtering happens in `liveObjectRowSource`: it keeps every loaded row and serves the rows the filter keeps. A filter change filters the loaded rows again at once, without a new list. Live updates go through the same path. `folder_objects.go` only records the row facts (name, ID, labels, health) on each `ObjectRow` and calls the filter step after a load.
6. Cursor and marks: the list is swapped like a live update, so the cursor stays on its row when the filter keeps it and otherwise stays at its position. The table already drops marks of rows that left the list, so hidden rows are unmarked and F8 cannot act on rows the user cannot see.
7. Frame: the active filter shows after the `ctx : ns` scope in the bottom border as ` Filter: <text>[, faults] ` (black on yellow). A click on it clears the filter.
8. The copied kubectl command (`M-c`) gets `-l <selector>` when a label selector is active. Name patterns have no kubectl equivalent and are not added.
9. F2 Options gets no text field for the filter; it gets the "Faults only" toggle (see ADR 0038). The dialog is reachable by key only.

## Alternatives considered

- `/` as in k9s: kc sends printable keys to the shell line, so `/` would steal the first character of every path typed there.
- mc shell patterns (globs) instead of regex: `+`/`-` already take globs for marking; k9s users expect regex, and a regex covers substring search with no extra syntax. Supporting both would need a prefix to tell them apart.
- Fuzzy matching (`-f` in k9s): left out; quick search covers jumping by prefix.
- Server-side label selector: the shared cache holds one informer per GVR without selectors. A selector would need a separate filtered informer or uncached LIST calls, both against AGENTS.md, and it would not save memory because the unfiltered informer stays for other folders.
- Filter per panel (mc keeps its filter across directories): a name regex rarely fits other resource kinds, and a filter that silently follows into the next list hides objects without the user asking for it.
- Filter in the list widget instead of the folder: the widget does not know labels or health, and the filter would be lost when the folder is re-set on the panel.

## Consequences

- Only object lists filter. Resource type lists, contexts and container lists do not; `M-f` there shows a toast. Extending it means implementing `RowFilterable` on those folders.
- Filtering costs one pass over the loaded rows per load or filter change; the rows are in memory already.
- Row facts are recorded at list time, so they are as fresh as the list (one reload per second at most while events arrive).
- Follow-ups: a "Filter..." entry in a future F9 pull-down menu (#48); keeping recent filters as history in the dialog.
