# Helm Releases

`/helm` lists the Helm releases of the panel's namespace scope. It is
a row near the top of the panel root and the first row of
`/namespaces/<name>`. Use it to see what a release deployed, with which
values, and whether its objects still exist, without the `helm` binary.

kc reads the release records Helm stores as Secrets (type
`helm.sh/release.v1`) from its shared cache. It never changes a
release.

## How to read it

- The release list has Name, Namespace, Revision, Status, Chart,
  App version and Updated, from the newest stored revision, as
  `helm list -a` shows them.
- A release folder is its newest revision. `values` (user-supplied),
  `manifest`, `notes` and `hooks` are the content stored in the
  release record, not the live state. The Stored content column and
  the viewer title say which revision they come from.
- `/resources` lists the objects of the stored manifest. Objects are
  found from the manifest, not from labels. The Live column says
  `yes`, `missing`, `denied`, `type not served` or `?`.
- `/history` lists every stored revision, newest first, with status,
  chart and description. Enter a revision for the same views of that
  revision.

## Keys

- `Enter` opens a release, a revision or `/resources`.
- `F3` on a release shows its stored status, like `helm status`.
- `F3` on `values`, `manifest`, `notes` or `hooks` shows that content;
  the viewer shows the matching `helm get` command.
- `F5` saves that content to a local file.
- `Enter` on an object in `/resources` (marked `@`, like a link in mc)
  opens the live object in the other panel. The focus stays on the
  Helm panel, so you can walk through the objects.
- `F3` on an object in `/resources` shows it as the release stored it.
- `M-c` copies the `helm` command shown for the folder.

## Limits

- kc needs list and watch on Secrets where `/helm` looks. In a
  namespace, a Role in that namespace is enough: kc then reads and
  watches Secrets in that namespace only. With all namespaces selected,
  list and watch on Secrets in all namespaces is needed. Without access,
  `/helm` shows one row that says what is denied and the `helm list`
  command to use instead; it never shows an empty list. A denial in
  one namespace does not affect another. After access is granted,
  leave and enter `/helm` again.
- Releases stored with `HELM_DRIVER=configmap` or `sql` are not shown.
- Computed values (`helm get values --all`) are not shown.
- The Live column is read once, with one GET per object, when
  `/resources` opens. Enter it again to read it again.
- kc does not install, upgrade, roll back or uninstall releases.

## See also

- [Viewer](viewer.md)
- [Panels](panels.md)
- [Access](access.md)
