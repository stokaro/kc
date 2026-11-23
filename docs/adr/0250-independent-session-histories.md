# 0250. Record CPU and memory session histories independently

- Status: Accepted
- Date: 2026-11-23
- Issue: #250

## Context

Info keeps a session history per scope, node or workload and draws a
sparkline under the CPU and the memory bar (ADR 0177, 0233). The issue
reports that a workload with a memory limit and no CPU requests or
limits gets a valid memory bar and no memory history. Confirmed on main:
`usagePercents` returns `gotCPU && gotMem`, and `Widget.record` returns
when that is false, so one resource without a percentage denominator
drops the samples of the other. The same happens the other way round.
Prometheus history is a separate path and is not affected.

## Decision

- `usagePercents` returns each resource on its own, nil when its bar
  shows no usage percentage. `record` stops only when both are nil.
- `history.add` takes both as pointers and adds only the measured ones.
  A missing resource adds no sample, never a zero, so its series shows a
  gap (`·`) or no history line, while the other series grows.
- The "same measurement read again" check moves from the history into
  each series (`series.last`), so a resource that comes back is recorded
  from its first new measurement and the other is not held back.
- A base change already resets only the series whose base changed; that
  stays as it is.
- The raw usage text of a bar without a percentage (for example
  `use 120m, no requests or limits`) is unchanged; that bar simply has no
  history line.

## Alternatives considered

- Record a zero for the missing resource. A zero reads as idle and would
  be plotted; the session history must never invent usage.
- Keep one timestamp for the pair. A later measurement of one resource
  would then have to be refused or would date the other one too.

## Consequences

- Memory-only and CPU-only workloads now get the history of the resource
  they have a percentage for.
- The window coverage line (ADR 0249) already judges CPU and memory
  apart; with this change it can report, for example,
  `CPU no samples, memory covered`.
