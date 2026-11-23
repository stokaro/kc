# 0198. Diagnostic commands

- Status: Accepted
- Date: 2026-11-23
- Issue: #198

## Context

Custom commands (F2, hotkeys) run a shell command line in a terminal panel.
Some tools print structured results: KRR prints CPU and memory
recommendations as JSON, and Inspektor Gadget traces DNS on the nodes. In a
terminal panel that output is text: it wraps, cannot be scoped to the
selected object, and a wrong formatter or an old tool version looks the same
as a clean result.

The issue asks for an adapter descriptor on custom commands, a KRR reference
adapter, and an Inspektor Gadget DNS action with confirmation, a read-only
check and a visible stop path. A failed or malformed run must not look like
an empty success. A running command must keep the scope it started with, and
selected names must not become shell syntax.

Running a tool on the cluster is a different risk from running a local one.
`kubectl gadget` needs a DaemonSet with privileged eBPF pods on every node,
or `kubectl debug node` pods, so it installs or starts privileged workloads.

## Decision

1. **A `diagnostic:` block on a custom command.** It has `adapter` (the output
   parser), `args` (the argument vector) and `timeout` (default 5m). Menu
   placement, `showFor`, `supportsMultiSelection`, `hotkey`, `prompts`,
   `confirm` and `dangerous` work as for any command. A diagnostic runs
   once, so `command`, `interactive`, `watchInterval` and `type: namespace`
   (which reruns on namespace changes) are refused with a message.
2. **No shell.** `args[0]` is the executable, looked up in PATH, and is not a
   template. Every other element is rendered as a template by itself and
   passed as one argument, so a name like `web; rm -rf /` stays one argument.
   The template data is always filled, even without a selection, and
   `.Namespace` falls back to the namespace the command runs in. The tool gets
   `KUBECONFIG` pointing at a generated kubeconfig for the panel's context and
   namespace, as other custom commands do.
3. **Availability.** kc checks the executable in PATH before it starts. A
   missing tool is reported as "`<tool>` is not installed or not in PATH; kc
   does not install tools". kc never installs anything, and never runs a
   diagnostic on its own (only from the menu or the hotkey).
4. **Immutable run.** When a run starts, kc fixes its context, namespace,
   rendered arguments and scope (the selected workloads, else their
   namespaces). Switching the selection or the context does not change it. The
   report header names that context and scope, so a late result is never
   read as one about the current panel.
5. **Outcomes.** The runner returns one of finished, unavailable, timed out,
   canceled or failed (non-zero exit, start error, or more than 32 MiB of
   output). A finished run is parsed. Output the parser does not understand
   gives "output not understood: …; kc shows no results for it", with the raw
   output below it. A timeout stops the tool (interrupt, then kill after
   3 s) and shows the end of its standard error. A canceled run shows a
   "stopped" note and no report.
6. **Stop path.** While a diagnostic runs, picking it again (menu or hotkey)
   opens a "Stop <name>?" dialog with its context, scope and command line.
   Quitting kc stops every run.
7. **Report.** A full-screen viewer shows the command, the start time and
   duration, the outcome, the report, the raw standard output and the end of
   standard error. The report starts with "Source: `<tool>` (output of an
   external tool, not checked by kc)". All tool text goes through a sanitizer
   that replaces control characters (ESC, BEL, CR, bidi controls, invalid
   UTF-8) with `?`. Tool output therefore cannot move the cursor, change
   colors or create OSC 8 links, and kc never treats it as a command.
8. **KRR adapter** (`adapter: krr`, for `krr simple -f json`). KRR has no
   output version, so the parser checks the shape: one JSON document with
   `scans` and `strategy.name`, and every scan with an object, kind, name,
   allocations and a recommendation, with values that are numbers, `"?"` or
   null. Anything else is an error. Rows show namespace, kind, name,
   container, live pods, and `current -> recommended` for CPU and memory
   requests and limits, with severity and KRR's per-resource info. kc
   formats the values as kubectl does (`250m`, `128Mi`). The summary gives the
   strategy, the observation window (`history_duration`) and every strategy
   setting. KRR's own errors are listed. kc limits the rows to the scope from
   point 4 and counts the scans outside it. kc never changes requests or
   limits, and the report says so.

## Deviations from the issue

- **No Inspektor Gadget action.** kc does not ship one. Tracing DNS with it
  needs either the gadget DaemonSet (privileged eBPF pods on every node,
  installed with `kubectl gadget deploy`) or node debug pods. Both are
  cluster-side installs that kc would start or depend on. They also fit badly
  in a run-once report: a trace streams until it is stopped. An operator who
  has deployed Inspektor Gadget can add it as an ordinary custom command:
  `kubectl gadget trace dns -n {{.Namespace}} -p {{.Name}}` with
  `dangerous: true` and `confirm: true`, in a terminal panel. There, read-only
  mode hides it, the red dialog asks first, and closing the panel stops the
  trace. kc adds no adapter for its output.
- **No "required capabilities" field.** The only capability kc can check
  without guessing is the executable in PATH. A field that names cluster
  components would need a check per tool, and that check is the tool's own
  error, which the report shows. Prometheus being unreachable is reported by
  KRR itself, among its errors.
- **No streaming output.** A diagnostic is finite. Streaming tools fit the
  existing terminal panel better.
- **No parser version field.** Adapters are built into kc and check the shape
  of the output instead, since KRR does not version its JSON.
- **No navigation from report rows.** The report is text in the viewer, with
  namespace, kind and name in each row. Rows that jump to objects would
  need a folder per adapter. That can come later if the report proves useful.
- **One run per command at a time.** Picking a running command offers to stop
  it rather than starting a second run with a different scope.
- **No KRR command in the defaults.** Most clusters have no Prometheus that
  KRR can reach, and most users do not have `krr` installed. The README has the
  command to copy.

## Alternatives considered

- **Parse the output of a terminal panel.** The panel is a terminal emulator.
  Its screen wraps and drops lines, so it is not the tool's output.
- **Keep `command` and parse its standard output.** It keeps the shell, so a
  selected name could still become shell syntax. An argument vector removes
  that risk.
- **Run KRR's logic inside kc.** That would duplicate a Prometheus client and
  KRR's strategies. The point of an adapter is that the tool stays the
  source.

## Consequences

- `CommandConfig` gains `diagnostic`. Commands without it behave as before.
- `internal/diagnostic` holds the runner, the report and the KRR parser. A
  new adapter is a parser function and a name in
  `appconfig.DiagnosticAdapters`; a test checks that every configured name has
  a parser.
- The report is plain text, so it can be searched and copied in the viewer.
- Follow-ups: rows that open the object they name; a Goldilocks or
  `kubectl-score` adapter if someone needs one.
