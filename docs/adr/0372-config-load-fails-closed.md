# 0372. A configuration file that does not load keeps read-only in force

- Status: Accepted
- Date: 2026-11-27
- Issue: #372
- Supersedes: decisions 1 and 2 of [0059](0059-config-load-errors.md)
  (a broken file is used not at all; the error is a toast), and in
  decision 2 of [0166](0166-info-panel-default.md), that a bad start
  mode makes kc ignore the file

## Context

`readOnly.enabled` and `readOnly.contexts` (ADR 0042) keep kc's own
changes away from the contexts a user protects. kc reads
`~/.kc/config.yaml` once, at start, through `appconfig.Store.Load`. Under
ADR 0059 any load error dropped the whole file: kc ran on the defaults,
where no context is read-only, and said so in a toast that lasted 15
seconds and was cut at the screen width. One typo in a layout value, or
an indentation slip anywhere, turned read-only off without a lasting sign.

The decoder is `sigs.k8s.io/yaml`: YAML to JSON, then `encoding/json`
into `Config`. Errors come from three places: the YAML parser (nothing
decodes), the JSON decoder (a value of the wrong type, or a type with its
own decoder that refuses the value, such as a duration), and the checks
that run after decoding (`layout` start modes, `charts.graphics`). The
JSON decoder named the field only for some type errors; a bad duration
said `json: cannot unmarshal number into Go value of type string`.

The issue leaves open whether kc should stop or run read-only, how much
of a broken file to keep, and how the error stays visible. kc has no
in-app reload (ADR 0059 rejected it), and read-only state is copied into
long-lived folder dependencies when a connection opens, so turning it off
in a running session is not a small change.

## Decision

1. **Fail closed.** Whenever the readOnly settings of the file are not
   in force, the configuration `Store.Load` returns makes every context
   read-only (`readOnly.enabled: true`). That is the case when the file
   cannot be read, is not valid YAML, is not a mapping at the top level,
   or its `readOnly` section does not load, and when kc cannot find the
   home directory. `LoadError.ReadOnlyLost` reports it. The rule lives in
   the loader, so every reader of the configuration gets it, and
   `App.readOnlyFor` stays the one place the UI decides. `--write` still
   turns read-only off for the run and `--readonly` still forces it on:
   a flag wins over the file, loaded or not.

2. **kc runs; it does not stop.** Every context read-only still lets the
   user look at the cluster and fix the file, and `--write` is there for
   a change that cannot wait. Stopping before the UI would make one bad
   layout value block every run.

3. **Keep what loaded, per top-level section.** When the whole file does
   not decode, kc decodes each top-level section (`layout`, `readOnly`,
   `commands`, ...) on its own onto the defaults, then runs the checks.
   A section that fails keeps its defaults as a whole; the others load.
   A file that decodes in one pass, as every valid file does, takes the
   same single decode as before, so valid files load exactly as they did.

   The unit is the section, not the field, because fields of one
   section depend on each other: keeping `nodeShell.enabled: true` while
   a bad `nodeShell.contexts` falls back to "every context", or a custom
   command without its bad `dangerous` flag, would make kc less safe than
   the file. A list (`commands`) is one value for the same reason.
   Sections are independent of each other except `readOnly`, which is
   decision 1.

4. **The error names the field.** For a section that fails, kc walks
   down the failing value (map keys in order, list items by index) to
   the first leaf that does not decode and names its path:
   `kubernetes.clusters.ttl: want a duration such as 30s or 2m, got 120`,
   `readOnly.contexts: want a list, got "prod"`,
   `commands[0].dangerous: want true or false, got "yes please"`. The
   checks keep their messages
   (`layout.rightPanelMode: unknown panel mode "describ"; use list, ...`).
   A YAML error keeps the parser's line (`yaml: line 3: did not find
   expected key`). `LoadError.Sections` names the sections that kept
   their defaults, and `LoadError.Reason` joins their problems on one
   line.

5. **A bar that stays.** While the file has not loaded in full, a bar
   between the panels and the command line, in the dark red of the
   frame's `RO` marker, says what kc does about it, the file and the
   problem, in that order, so a narrow screen cuts the least important
   part:

   ```
   Config not loaded, every context read-only: ~/.kc/config.yaml: yaml: line 3: did not find expected key
   Config layout not loaded, using its defaults: ~/.kc/config.yaml: layout.rightPanelMode: unknown panel mode "describ"; ...
   ```

   The path under the home directory is written with `~`. The bar wraps
   to two rows at most (one below 16 rows, none below 8, like the command
   line) and ends in `…` when the text is longer; the log has all of it.
   The panels give up those rows. Toasts keep using the command line, so
   they do not hide the bar. The bar replaces the 15-second toast. Like
   the command line, it is not drawn over the full-screen terminal or a
   full-screen viewer.

6. **Until the file loads, which is the next start.** kc reads the file
   once, so the bar and the fail-closed state last for the session. A
   file fixed while kc runs takes effect at the next start. Saving is as
   in ADR 0059: `C-s` reads the file again and refuses while any section
   does not load, since writing kc's defaults for that section would
   replace what the user wrote there; after a fix, `C-s` writes into the
   fixed file.

## Alternatives considered

- **Stop before the UI with the error on stderr**, as ADR 0371 does for
  an undefined current-context. Safe, but a cosmetic typo would make kc
  unusable until fixed, and a user who needs to look at a cluster now
  could only move the file away, which turns read-only off. Running
  read-only keeps them safe and working.
- **Fail closed only for a YAML error, keep 0059 otherwise.** A type
  error in `layout` would still drop `readOnly.contexts`; that is the
  case the issue reproduces.
- **Keep every field that decodes.** Field by field, as the lenient
  parser ADR 0059 removed did. It mixes file values and defaults inside
  one section in ways that can widen what a setting allows (decision 3).
- **Decide read-only in the UI from the load error** instead of in
  `Store.Load`. It keeps the loaded config literal, but any reader that
  forgets the error gets the unsafe value; `appconfig.Load` has callers
  outside the UI.
- **Reload the file while kc runs** (a watch or a menu entry). It would
  end the read-only state as soon as the file is fixed, but read-only is
  copied into folder dependencies when a connection opens, and re-applying
  every other setting to live panels is the reload ADR 0059 deferred.
  Restart covers it.
- **Show the error in the function key bar or the panel frame.** The
  function bar has no room for a path and a field next to ten keys, and
  the frame belongs to one panel while the error belongs to the session.
- **A toast that stays until dismissed.** Toasts share the command line
  with every other message, so the next one would replace it.

## Consequences

- One bad value no longer turns read-only off. A file kc cannot read at
  all makes every context read-only until it is fixed and kc restarts, or
  until `--write`.
- A file with one broken section keeps the others: the user sees their
  settings minus that section instead of all defaults.
- A broken file costs a row or two of panel height for the session.
- Problems with values a section decodes but kc checks later (columns,
  health rules, context colors, skins) are unchanged: they are reported
  and skipped entry by entry, as before.
- Follow-up: an in-app reload would let the bar and the fail-closed state
  end without a restart.
