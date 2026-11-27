# Table component

This directory is the reusable `table` package. Follow the root `AGENTS.md`;
use the repository's `kc-tui` skill for event, layout, or selection changes.
Paths in this file are relative to `internal/table` unless stated otherwise.

## Entry points and contracts

- `model.go`: `Row`, `List`, `Column`, `SimpleRow`, and the slice/list providers.
- `bigtable.go`: `BigTable`, rendering, focus, scrolling, and selection.
- `linked_provider.go`: adapts the linked provider to windowed access.
- `demo/main.go`: the runnable synthetic-data demo. It needs no cluster.

Read the current interfaces in `model.go` before implementing a provider:

- `Row.Columns() (id string, cells []string, styles []*lipgloss.Style, exists bool)`
  returns a stable ID, ASCII cell strings, and per-cell styles. Keep ANSI styling
  out of the cell data. Do not broaden the cell contract as an incidental fix.
- `List` provides `Lines(ctx, top, num)`, `Above(ctx, rowID, num)`,
  `Below(ctx, rowID, num)`, `Len(ctx)`, and `Find(ctx, rowID)`. Every method takes
  `context.Context`; `Find` returns `(int, Row, bool)`.
- Construct a slice provider with `NewSliceList([]Row{SimpleRow{ID: "a"}})`.
  Use the actual provider APIs rather than copying outdated examples.

## Change invariants

- Keep reads windowed for large datasets; avoid materializing every row during
  paint. Preserve cursor and marks by row ID when data is reordered or replaced.
- The parent routes keys to the focused table. Preserve that boundary when
  changing focus, selection, or mouse handling.
- Check both `ModeFit` and `ModeScroll`, resize, empty data, deleted focused
  rows, and the changed width/border condition. Use the nearby focused tests;
  `view_safety_test.go` and `view_height_explicit_test.go` cover rendering edges.
- Keep dependency and formatting policy in the root guide. Existing
  `REQUIREMENTS.md` describes the product contract; current source defines
  signatures when older documentation omits arguments.

## Commands from the repository root

- Tests: `make test-unit PACKAGES='./internal/table'`.
- Build component and demo: `go build ./internal/table/...`.
- Interactive demo: `go run ./internal/table/demo slice` or
  `go run ./internal/table/demo linked`.

Do not run the whole repository suite merely to exercise this isolated package;
use the root verification policy when a change also affects its consumers.
