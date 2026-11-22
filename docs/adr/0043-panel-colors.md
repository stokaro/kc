# 0043. Per-context panel colors and panel skins

- Status: Accepted
- Date: 2026-11-22
- Issue: #43

## Context

k9s lets users skin the whole UI and pick a skin per context, so a production
cluster looks different. kc had one fixed palette for its panels: Midnight
Commander blue, hard-coded in `internal/ui/styles` and inline in the panel
code. Only the viewer had a theme (`viewer.theme`).

The issue asks for a `contexts.<name>.color` setting (or a pattern) that
tints the panel frame and the `ctx : ns` label, and later for an mc-like skin
file. It leaves open: the config shape and pattern syntax, the skin format
and location, which UI elements a skin covers, which skins ship, and how bad
input is handled. With no config kc must look exactly as before.

## Decision

### 1. Per-context colors: an ordered list, first match wins

```yaml
contexts:
  - match: "*prod*"
    color: red
  - regex: "^gke_.*_europe-"
    color: "#ff8800"
```

Each entry sets exactly one of `match` and `regex`, and a `color`.

- `match` is an exact context name or a glob. `*` matches any run of
  characters, `?` one character; everything else is literal. The glob must
  match the whole name. Unlike `path.Match`, `*` also matches `/`, because
  EKS context names are ARNs (`arn:aws:eks:...:cluster/prod`).
- `regex` is a Go regular expression, matched anywhere in the name unless
  anchored.
- Matching is case-sensitive, like kubeconfig context names.

The color tints the frame lines of the panel (top, sides, the bottom border
and the footer box) and fills the context label in the bottom border, with
black or bright white text, whichever reads better on that color. The
namespace label and the panel background stay as they are. While a command
line has focus the panel keeps its grey look and the tint is not shown.

### 2. Skins: YAML files with the panel palette

`panel.skin` names the skin. kc looks for `~/.kc/skins/<name>.yaml` first and
then for a built-in skin of that name, so a user file can replace a shipped
skin. An empty value means `mc`. Names may only use letters, digits, `.`,
`_` and `-`, which keeps the lookup inside the skins directory.

```yaml
panel:
  background: blue
  foreground: white
  frame: white
  header: yellow
  marked: yellow
  cursor: {foreground: black, background: cyan}
  title: {foreground: black, background: white}
  context: bright-yellow
  namespace: bright-cyan
```

Every color is optional and falls back to the `mc` value, so a skin can
change one thing. Colors are ANSI names (`red`, `bright-cyan`, `gray`), ANSI
256 indexes (`0`-`255`) or hex (`#rrggbb`, `#rgb`). The same syntax applies
to `contexts[].color`.

### 3. A skin covers the panel area only

Background, text, frame lines, table header, marked rows, cursor row, the
active panel's path label, and the context and namespace labels. Dialogs,
the function key bar, the command line and the viewer are not covered.

### 4. Built-in skins: `mc` and `dark`

`mc` is exactly the current palette (a test checks this); `dark` puts the
panels on black with grey frame lines. Both are embedded YAML files that also
serve as examples to copy.

### 5. Bad input is reported, never ignored

Unknown keys, bad colors and unknown skin names are errors. Every bad field
is listed, not only the first. kc does not refuse to start: a bad skin falls
back to `mc`, a bad `contexts` entry is skipped while the good ones apply,
and the error is shown as a red message at startup and written to the log.

### 6. The palette is global; the tint is per panel

`uistyles.Apply` sets the palette once at startup and rebuilds the shared
panel styles. The context tint is held by each panel and follows its scope,
so it changes when the user switches context.

## Alternatives considered

- **`contexts.<name>.color` as a map, as the issue sketches.** YAML maps have
  no order, so with patterns as keys two matching entries have no defined
  winner. A list with first-match-wins is predictable and still reads well.
- **One `match` field that detects a regex by `/.../` or a `re:` prefix.**
  Context names can contain `/` and `:`, so any marker risks misreading a real
  name. Two separate fields are explicit.
- **`path.Match` globs.** `*` stops at `/`, so `*prod*` would miss EKS ARNs.
- **mc ini skins.** mc skins describe the whole mc UI with mc-specific keys
  (`[core]`, `[dialog]`, `[filehighlight]`, ...) and color names that kc would
  have to map one by one. kc's config is YAML already, and only the panel
  area is skinnable today. A converter can come later.
- **Inline skins in `config.yaml`.** It adds a second place to look for the
  same data. A skin file is easy to share and to switch; per-context color
  covers the common "make prod red" need without a skin.
- **Per-context skins (k9s style).** The panel widgets read shared style
  values; giving each panel its own palette means threading it through every
  widget and the table renderer. The tint covers the main need. Per-context
  skins can build on the palette type later.
- **Refuse to start on bad config.** A typo in a color should not lock the
  user out of kc; a visible error plus the default look is enough.
- **Tint the whole panel background.** Red or yellow backgrounds make the
  tables hard to read; frame lines and the context label are enough to tell
  clusters apart, and the default stays visually unchanged.

## Consequences

- With no config kc looks as before: `panel.skin: mc` is the default and
  `contexts` is empty.
- Panel code reads colors from `uistyles.Current()` instead of literals. New
  panel code should do the same so skins keep working.
- `uistyles.Apply` mutates package state and is not safe for concurrent use.
  It runs once in `App.Init`; tests that call it are not parallel and restore
  the default palette.
- Skins cannot yet color dialogs, the function key bar or the command line,
  and there is no per-context skin. Both are possible follow-ups on top of
  `uistyles.Palette`.
