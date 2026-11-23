# Compare lists

`C-x d` compares the object lists of the two panels, as mc's "Compare
directories". `C-x c` compares the active panel's list with the same
resource and namespace in another kubeconfig context. Use them to see
which Deployments differ between a staging and a prod namespace, or
between the staging and the prod cluster. Nothing here changes the
cluster.

## How it works

- `C-x d`: each panel must show an object list of the same resource and
  API version, for example `deployments.apps` v1 on both sides. kc does
  not convert between versions and says so when they differ.
- Objects are matched by name. When both lists span all namespaces,
  they are matched by namespace and name. A list of all namespaces
  cannot be compared with a list of one namespace.
- Two different namespaces pair with each other by name. The title then
  says `(metadata.namespace ignored)`.
- `C-x c` opens a list of the other contexts. kc connects to the one
  you pick only for the read, with kc's impersonation. It does not
  switch to that context, and no panel shows it.
- Each pair is compared in the configuration view of `C-x C-d`: server
  fields and `status` are left out. The comparison takes the whole
  lists; a row filter does not narrow it.

## How to read it

The result is a list in a dialog. Its title names both sides, for
example `left staging: deployments.apps in shop | right prod:
deployments.apps in shop`. The command line shows the counts:
`2 changed, 1 only left, 0 only right, 0 unavailable, 9 equal`.

Each row has a state and the object's name:

- `changed`: on both sides, the manifests differ.
- `only left` or `only right`: on one side only.
- `unavailable`: kc could not compare it, with the reason. For example
  a name that occurs twice on one side.
- `equal`: no difference in the configuration view.

Changed rows come first, then only left, only right, unavailable and
equal.

Across clusters, fields with generated values, such as owner reference
UIDs, often make a pair `changed`. Open the diff to see why.

## Keys

- `C-x d` compares the two panels; `C-x c` compares with a context.
- "Compare lists" and "Compare with context" in the Command menu
  (`F9`) do the same.
- Typing filters the result list.
- `Enter` on a pair opens its manifest diff; `Esc` there returns to the
  list. On a one-sided or unavailable row, `Enter` says why.
- `Esc` closes the list.

## Limits

- The result is a snapshot; run the comparison again to reread.
- If one side cannot be read (forbidden, not served there, a timeout
  after 30 seconds), the comparison fails with the reason. kc never
  reports those objects as only on the other side.
- Marked rows are not compared on their own; the whole list is.

## See also

- [Compare objects](compare-objects.md)
- [Diff viewer](diff-viewer.md)
- [Context and namespace](context-namespace.md)
