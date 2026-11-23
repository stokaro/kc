# 0192. No offline investigation captures for now

- Status: Accepted
- Date: 2026-11-23
- Issue: #192

## Context

The issue asks for a portable record of an investigation: a versioned,
sanitized capture file with object summaries, revisions over a recording
window, measured samples and coverage metadata. kc would reopen the file as
a read-only offline investigation with lists, object details, a timeline and
revision diffs, with every live operation disabled. Optional event messages
and log slices would be added after a preview.

The capture's value depends on two properties. It must contain only fields
known to be safe, and the offline view must never act on a cluster. To see
what each part would take, we checked the issue against the current code:

- **What kc shows is not allowlisted.** Object lists are the server's Table
  output (server-side printing, as `kubectl get` shows it). Cells come from the API server
  and from CRD `additionalPrinterColumns`, so a CRD author decides what a
  column holds. User columns (`columns:` with `jsonPath`) can read any
  field, including environment literals. Event rows carry free-form
  messages. A capture built from what the panels show would not meet the
  issue's own rule ("save only allowlisted diagnostic fields; do not
  serialize whole raw objects and redact them afterwards"). Meeting it needs
  a new summary model per kind: which fields of a Pod, a Deployment, a Node,
  a Service, and so on, are safe. Unknown kinds and CRDs would get identity
  and conditions only.
- **Revisions and a timeline are recordings over time.** kc does not record
  object changes today. Recording them during an investigation is #191, which
  another lane owns. A capture without it holds one point in time, and
  "captured revisions" and "recorded timeline" would be empty or invented.
- **An offline mode is a second data layer.** Every folder (contexts,
  resource groups, object lists, Info, Describe, Manifest, related objects,
  overview) reads the shared cache or the API. An offline view needs file
  backed versions of each, and every action has to know about offline
  selections: F4, F8, F7, M-x, M-t, M-p, the debug helper, the node shell,
  copies into containers, object actions and custom commands. If one guard is
  missed, a key that looks harmless acts on the live cluster of the same
  name. That is the failure the issue warns about.
- **Pinning and sharing an investigation window is #187**, also with another
  lane. A capture's notion of "the investigation" should follow it, not
  define it first.

## Decision

kc does not add capture files or an offline mode now. This ADR closes #192.

What an operator can already do keeps working and is the honest answer for
today:

- `F5` saves a manifest, logs, ConfigMap or Secret data or a file to a local
  file atomically with mode 0600 (ADR 0057). The operator chooses each
  source, so sensitive content is saved only on purpose.
- `M-c` copies the kubectl command of a list or an object, and the overview
  shows and copies the commands behind each finding, so the same evidence
  can be collected again or by someone else.
- Local panels (`M-l`) and `F3` show saved files, offline, without a cluster.

## Alternatives considered

- **Snapshot the visible lists into one JSON file and show it in the viewer.**
  It is small, and the viewer cannot act on a cluster. But it saves server
  Table cells, which are not allowlisted (see above). It would also be
  presented as a sanitized capture, which it is not. Saving only names,
  namespaces, GVRs and ages would be safe, but it records too little to
  review an incident.
- **Allowlisted summaries for core kinds only, without offline browsing.**
  This is the most likely first step. It still needs the per-kind field
  lists, a format version, size limits and a preview of optional sources.
  Without #191 it holds one point in time. It is better built on top of
  #191's records than next to them.
- **Full scope as asked.** It touches the data layer, every action guard and
  two other lanes' features at once. The safety properties that justify it
  would be hard to review in one change.

## Consequences

- No new file format, so nothing to keep compatible yet.
- Follow-ups, in this order, once #191 and #187 have landed:
  1. A per-kind allowlist of summary fields (identity, owner references,
     conditions, phase, restart counts, requests and limits, node, images),
     tested with deliberately sensitive fixtures (Secret data, ConfigMap
     values, env literals, annotations).
  2. A versioned capture of the pinned investigation window: these summaries,
     the revisions #191 recorded, coverage and errors (forbidden, not
     listed, dropped), written atomically with mode 0600 and size bounds.
     Event messages and log slices go in only after a preview.
  3. Opening a capture as a read-only report in the viewer first. A navigable
     offline mode only if the report proves too limited, with offline items
     as their own types that carry no cluster reference, so live actions do
     not apply to them by construction.
