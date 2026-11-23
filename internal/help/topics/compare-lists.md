# Compare lists

`C-x d` compares what the two panels show, as mc's "Compare
directories". `C-x c` compares what the active panel shows with the same
resource and namespace in another kubeconfig context. `C-x a` compares
the two whole lists. Use them to see
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
  switch to that context, and no panel shows it. kc works in one
  context at a time; this is a read of another context, not a second
  live panel.
- Each pair is compared in the configuration view of `C-x C-d`: server
  fields and `status` are left out.

## What is compared

Each side is one of:

- `marked`: the rows marked in the panel (`Ins`, `+`, `C-a`), the same
  rows `F5` and `F8` act on. Marks win over a filter.
- `visible`: with no marks and an active row filter (`M-f`, `C-z`),
  the rows the filter keeps, exactly as the panel shows them.
- `whole list`: with neither, or with `C-x a`, every object the list
  read returns.
- `counterparts`: a side without marks or filter facing one with them
  (the other panel, or the other context of `C-x c`) contributes only
  the objects with the same keys. Without this, every object the other
  side hid would be reported as "only" on this side.

kc freezes the selection when you press the key: the namespace, name
and UID of each row. Objects created later are not added. A selected
row whose object was deleted, or replaced by a new object with the same
name (another UID), before the read is `unavailable` with that reason,
on both sides: kc does not count it as only on the other side.

The title names the scope and the counts of each side, for example
`left staging: deployments.apps in shop [visible 2 of 10 (filter: web)]`
and `right prod: deployments.apps in shop [counterparts 2 of 12]`.

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

- `C-x d` compares the two panels; `C-x a` compares their whole lists;
  `C-x c` compares with a context.
- "Compare lists", "Compare whole lists", "Compare with context" and
  "Compare whole list with context" in the Command menu (`F9`) do the
  same.
- Typing filters the result list.
- `Enter` on a pair opens its manifest diff; `Esc` there returns to the
  list. On a one-sided or unavailable row, `Enter` says why.
- `Esc` closes the list.

## Limits

- The result is a snapshot; run the comparison again to reread.
- If one side cannot be read (forbidden, not served there, a timeout
  after 30 seconds), the comparison fails with the reason. kc never
  reports those objects as only on the other side.
- A filter on a column (`cpu>500m`) uses the cells the panel shows, so
  it cannot be applied to another context. The other context therefore
  contributes counterparts, not its own filtered rows.

## See also

- [Compare objects](compare-objects.md)
- [Diff viewer](diff-viewer.md)
- [Context and namespace](context-namespace.md)
