# Architecture Decision Records

This directory records decisions that an issue left open or that chose between real alternatives: key bindings, UX shape, data sources, scope cuts, config shape.

## Naming

`NNNN-short-slug.md`, where `NNNN` is the GitHub issue number the decision belongs to, zero-padded to four digits (for example `0037-column-sort.md` for #37). Decisions that do not come from an issue use the number of the pull request that introduces them. Using the issue number keeps parallel pull requests from fighting over the next free number. One file per issue; several decisions go into it as numbered sections.

A decision is not edited after it is accepted. When it changes, write a new record and set the old one's status to `Superseded by NNNN`.

## Template

```
# NNNN. Title

- Status: Proposed | Accepted | Superseded by NNNN
- Date: YYYY-MM-DD
- Issue: #NNNN

## Context
The problem, the constraints, and the questions the issue left open.

## Decision
What kc does, concretely: keys, config keys, behavior.

## Alternatives considered
Each option and why it was not chosen.

## Consequences
Trade-offs, follow-up work, what becomes easier or harder.
```

## Records

List them with `ls docs/adr`. File names sort by issue number.
