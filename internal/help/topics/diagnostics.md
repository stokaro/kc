# Diagnostic commands

A diagnostic command is a custom command with a `diagnostic:` block. It
runs a local tool once and shows its parsed output in a full-screen
report instead of a terminal panel. kc ships one adapter, `krr`, for the
JSON output of KRR (`krr simple -f json`), which recommends CPU and
memory requests and limits from Prometheus history.

```yaml
commands:
  - name: "KRR recommendations"
    type: "sticky"
    showFor:
      resources: ["deployments", "namespaces"]
    hotkey: "C-x k"
    diagnostic:
      adapter: krr
      args: ["krr", "simple", "-f", "json",
             "--logtostderr", "-n", "{{.Namespace}}"]
      timeout: 10m      # default 5m
```

## How it works

- `args` is run without a shell. The first element is the executable,
  looked up in `PATH`. Every other element is a template passed as one
  argument, so a selected name never becomes shell syntax.
- The tool gets `KUBECONFIG` for kc's context and the namespace, like
  other custom commands.
- A run keeps the context, namespace and selection it started with.
  Moving the cursor or switching context does not change it.
- kc never installs a tool and never runs a diagnostic on its own. A
  missing executable is reported when you pick the command.

## How to read the report

The report starts with the context, the scope (the selected workloads,
a namespace or the whole context), the command line, and when it
started and how long it took. Then the outcome:

- finished: the report follows. It starts with `Source:` and the tool,
  a reminder that kc did not check the values.
- `output not understood`: kc could not parse the output and shows no
  results. It never shows such output as an empty result.
- `failed`: with the exit status and the end of standard error.
- `timed out`: kc stopped the tool and discarded its output.

Below the report are the tool's raw output and the end of its standard
error. Control characters in tool text are shown as `?`.

The KRR report lists each container's CPU and memory requests and
limits as `current -> recommended`, with KRR's severity, and `?` with
the reason when KRR has no recommendation. The header gives the
strategy and its observation window (`history_duration`). kc only shows
what KRR returns and never changes requests or limits.

## The stop dialog

While a diagnostic runs, picking it again (from `F2` or its hotkey)
opens a red dialog, "Stop KRR recommendations?", with its context,
scope, command line and how long it has run. `Stop` interrupts the tool
and discards its output. Quitting kc stops every run.

## Keys

- `F2` or the command's hotkey starts it, or offers to stop it.
- In the stop dialog `Cancel` has the focus; `Tab` then `Enter` stops.
- The report is a viewer: search, copy and close work as there.

## Limits

- One run per command at a time.
- `command`, `interactive`, `watchInterval` and `type: namespace` do
  not apply to a diagnostic.
- KRR needs a Prometheus with enough history.
- kc has no adapter for cluster-side tools such as Inspektor Gadget.
  Add one as a plain custom command with `dangerous: true` and
  `confirm: true`.

## See also

- [Custom commands](custom-commands.md)
- [Confirmation dialog](confirm.md)
- [Viewer](viewer.md)
