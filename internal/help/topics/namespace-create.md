# Create namespace

`F7` in the `/namespaces` list opens a small dialog that creates a new
namespace, as `kubectl create namespace <name>` does. It is the one
object kc creates from a key, as mc's `F7` makes a directory.

## How it works

- Open `/namespaces` from the panel root, then press `F7`. The function
  bar labels the key "Namespace".
- Type the name and press `Enter` or choose Create.
- kc checks the name before it sends the request and shows what is
  wrong in the dialog. It must be a DNS label: lowercase
  letters, digits and `-`, at most 63 characters, starting and ending
  with a letter or digit.
- On success the message says "Namespace <name> created" and the list
  shows it. A `/namespaces` list in the other panel is updated as well.
- A refusal from the API server, such as forbidden or already exists,
  is shown as a red message with the server's reason.

## Keys

- `F7` (or `Esc 7`) opens the dialog in `/namespaces`.
- `Enter` creates the namespace.
- `Esc` cancels.

## Limits

- `F7` works only in `/namespaces`. Elsewhere in a list it is grayed
  out; in viewers `F7` is search.
- The key is grayed out when kc's access check says you may not create
  namespaces in the current context.
- Read-only mode refuses it with "Read-only mode: Create namespace is
  disabled".
- The dialog sets only the name. Labels, such as a pod security level,
  need `F4` on the new namespace or kubectl.
- Creating a namespace does not change the panel's namespace scope.

## See also

- [Context and namespace](context-namespace.md)
- [Read-only mode](read-only.md)
- [Access](access.md)
- [Delete](delete.md)
