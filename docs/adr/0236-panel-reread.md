# 0236. Panel reread (C-r)

- Status: Accepted; with text on the shell line `C-r` goes to the shell (superseded by [0376](0376-keys-reach-the-shell.md))
- Date: 2026-11-23
- PR: #236

## Context

In mc, C-r rereads the active panel. kc listed C-r as a panel key, but it
only cleared the list widget's remembered positions. Most folders do not
need a reread to stay current: they are fed by the shared controller-runtime
cache and its informers. Some keep state that events do not update:

- the local filesystem and container directories are listed once;
- lists of resource types keep the API discovery until the 30 s refresh;
- a live list waits at least a second between two loads, and gives up its
  list and watch after a forbidden answer;
- Describe skips the describe requests while the resourceVersion is the
  same, although its events section changes without it;
- the overview and Info recompute after an event or every 30 s.

## Decision

- C-r, and Left/Right > Reread, read again what the active panel shows. The
  App handles the key, so it works in every mode and the panel's list
  widget no longer sees it.
- Folders implement `models.Rereader` (`Reread(ctx)`), next to `Refresh()`.
  `BaseFolder.Reread` marks the rows dirty and lets the row source drop its
  state: a once-read source lists again, a live source loads at once,
  retries a list and watch given up after forbidden, and keeps a running
  watch. No new watch or informer starts for a reread.
- A list of resource types also invalidates the cached discovery
  (`Cluster.RefreshDiscovery`), so a new CRD shows at once. It logs an INFO
  line before the request.
- Overview folders and Info ask the overview engine to recompute on its
  next tick. They read the shared cache.
- Describe and Manifest use `reload.Loader.Reread`: the load starts at once,
  without the one-second interval and without the known version, so Fetch
  cannot skip it.
- Logs does not reopen its stream. A live stream has nothing to reread, a
  resumed one reconnects by itself, and reopening an ended stream would
  replace the kept lines with the tail of a new one. C-r says which case
  applies; choosing Logs again still reopens an ended stream.
- A Command panel does not run its command again, because a command can
  change the cluster. A frozen panel stays frozen.
- The F3 viewer reads its content again off the UI goroutine and keeps the
  scroll position. A file shown whole is read with the same size limit as
  the first time; one that grew past it is not read whole, and F3 opens it
  again in pages. A paged file is opened again, because an editor may have
  replaced it and the open handle would still read the old file. The
  cached blocks, the size and the line count are dropped, reads of the old
  file are ignored, and the position stays, moved up to the last page when
  the file got shorter. A paged file that is now small stays paged.
- Widgets that reread report the end with `panelcontent.RereadDoneMsg`; the
  App shows a short confirmation or the error.
- In the full-screen terminal C-r goes to the shell (history search). With
  text on the shell line it still rereads the panel, as in mc.

## Consequences

C-r costs at most one discovery round, one cache read or one describe per
press. Retrying a forbidden list costs one request per press. A list's
confirmation appears when the reread starts: a live list loads in the
background and repaints when it is done.
