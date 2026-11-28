# 0380. A list says why it is empty

- Status: Accepted
- Date: 2026-11-28
- Issue: #380 (the lists and the error texts; the frame of a bad
  connection, the message log and port forwards of deleted pods are
  separate changes)

## Context

A list that could not be read looked like a list with nothing in it.
After `Enter` on a failed start, the left panel said "No items"; a list
the user may not read showed only its back row; `kc -n shpo` with a
mistyped namespace showed the types and empty lists, as for an empty
namespace. The reason went to a toast that disappeared, and the toast
carried the raw client error: the request URL with its query, then
`dial tcp 10.0.0.1:6443: connect: connection refused`. A 403 from the
table list could not even be decoded and read `unknown (get configmaps)`.
Without a kubeconfig, `Run` printed a warning that the UI then covered.

## Decision

1. **The notice is drawn in the list, below the rows**, on the lines the
   table fills with empty rows: the reason in red, wrapped at the
   panel's width, and below it, in the text color, what helps:
   `C-r retries · Context... (Left/Right menu) picks another` for a
   connection, `C-r reads again` for a forbidden list, `Namespace...
   (Left/Right menu) picks another` for a missing namespace. Rows that
   fill the panel are drawn as they are, without the notice.
2. **A folder says why through `models.ProblemFolder`.** `ListProblem`
   reads memory only, so `View` can call it. It is the error of the last
   load when that load left the list empty, for the live object lists
   and the folders that populate on read (the root among them). A
   failed reload that keeps earlier rows is no reason for an empty list,
   and neither is a row filter that hides every row. A load that waits
   for API discovery (`ErrDiscoveryPending`) is shown as "Reading the API
   discovery…" in the text color.
3. **A missing namespace is asked for, once per empty load.** A
   namespaced list whose load listed nothing sends one `GET` for its
   namespace, bounded by 5 s, in the same background load; the root asks
   when it is built and on `C-r`. Only `NotFound` is named; a namespace
   the user may not get, or a failed request, says nothing. The request
   is logged before it is sent.
4. **Errors are said in plain words** by `internal/kubeerr`: `Cannot
   reach 10.0.0.1:6443: connection refused` (also: no route to the host,
   the connection was reset, no such host, no answer in time, the server
   is unavailable (503)), `10.0.0.1:6443 rejected the credentials (401
   Unauthorized)`, `The certificate of 10.0.0.1:6443 is not trusted:
   signed by an unknown authority` (or not valid for the host, or
   expired), `Not allowed to list configmaps in team-a (user dev)` from
   the RBAC message, and `Namespace shpo does not exist`. The host
   replaces the request URL. Anything else keeps its text on one line.
   The list notices, the start error screen, the `Cannot connect`,
   `Cannot switch` and API discovery messages, the read failures of
   lists ("Could not read pods in shop: cannot reach ...") and Info's
   capacity and top-pods reasons use these texts.
5. **The table list decodes error answers.** Its default scheme now
   registers the meta types, so a refused request keeps the server's
   message, which names the user, the verb and the namespace.
6. **Without a kubeconfig the panels name the file** kc looked for, in
   the lists and in Info: `No kubeconfig: $KUBECONFIG=<path> not found`,
   `No kubeconfig: <home>/.kube/config not found`, or `No contexts in
   <files>`. `Run` no longer prints a warning before the UI starts. A
   client that cannot be built for the start context is shown the same
   way, as `Cannot connect to <context>: <reason>`.
7. The message after a failed API discovery at startup reads `Cannot
   read the API discovery of <context>: <reason>`; it was `Discovery
   resources failed: <error>` (0378, startup, point 5).

## Alternatives considered

- **A row in the table.** A row is cut at the column width instead of
  wrapping, and the cursor, marks, sort and filters would treat it as an
  object.
- **Keep the toast only.** It disappears after seconds and is not where
  the user looks; the list still reads as empty.
- **Ask for the namespace at startup only.** It misses a scope set later
  through Namespace... or `cd -n`, and the root of every scope would need
  its own answer anyway.
- **Ask access reviews before each list.** The list's own 403 already
  names the user and the verb; a review would add a request per list.

## Consequences

- An empty namespaced list costs one more `GET` per load; a list with
  rows costs nothing more.
- The readable forbidden text depends on the RBAC message format; other
  authorizers fall back to `Not allowed: <reason>`.
- The overview folders keep their own reasons in their rows and draw no
  notice.
- A failed reload that keeps its rows still shows them as current; the
  frame of a bad connection and stale cells are the next change for
  #380. A merged log of a user without list and watch on pods stays
  silent until the informer's error is surfaced (#379).
