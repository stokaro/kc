# Field Documentation

`M-e` (or `Esc e`) on an object row or a resource type row opens the
documentation of that type's fields in the same panel, at
`/explain/<group>/<version>/<kind>`. It works like `kubectl explain`
and covers custom resources too, so you can look up a field without
leaving kc.

The schema comes from the connected cluster (`/openapi/v3`, the source
`kubectl explain` uses), for exactly the group and version the panel
lists. A kind that exists in two API groups shows the right one.

## How to read it

- Each row is a field: Name, Type, Required and the first sentence of
  its description.
- Types read as in kubectl: `string`, `[]Container`,
  `map[string]string`, `IntOrString`.
- A field with nested fields shows as `/field`. Arrays and maps lead
  to the fields of their items, as in `kubectl explain`.
- The command line shows the matching
  `kubectl explain <resource>.<field> --api-version=<group>/<version>`.

## Keys

- `Enter` on a `/field` row lists its nested fields.
- `F3` on a field shows its full description, with enum values,
  default, format and list type.
- `/..` goes up one level; at the top it returns to the row you
  started from.
- `M-c` copies the `kubectl explain` command.

## Limits

- "No documentation" means the cluster publishes no schema for the
  type. Aggregated APIs often do not.
- "No fields documented" means the schema has no properties at that
  level, for example a field that keeps unknown fields.
- "Schema request failed" shows the error of the request, for example
  when reading `/openapi/v3` is forbidden.
- kc keeps the last 16 schema documents in memory. Every `M-e` reads
  the small `/openapi/v3` index, so CRD updates and other contexts
  show without a restart.
- If the panel moved before the schema arrived, kc does not open it;
  press `M-e` again.

## See also

- [Manifest mode](mode-manifest.md)
- [Viewer](viewer.md)
- [Panels](panels.md)
