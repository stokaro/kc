# Agent workflow reference

Read the section needed for the current task. The root [AGENTS.md](../AGENTS.md)
contains the always-applicable rules; this guide is not additional startup
reading. Go, TUI and Kubernetes skills hold their domain-specific contracts.

The choices and tradeoffs are recorded in
[ADR 0392](adr/0392-agent-skills-and-quality-checks.md).

## Execution and context

Use one implementer for an ordinary change. Identify the affected files,
behavioral contract and checks in a short plan. A larger design deserves a
written decision when it changes an interface, ownership or user behavior;
an already-decided small fix does not need another design approval.

Delegate independent investigations or one review of a substantial change
when a separate perspective justifies rebuilding context. Supply the task,
relevant paths, contracts and diff rather than the conversation history. Keep
implementations that touch the same state or files sequential. Reviewers should
use valid recorded test evidence, rerunning checks only to resolve a specific
doubt or changed code. Avoid recursive reviewer chains.

Keep detailed logs in temporary files and expose the failing section, command
and exit status. Reuse evidence on unchanged code; a bookkeeping-only commit
does not invalidate it. A dependency, fixture, environment or relevant source
change can. Report the distinction between a focused pass and full verification.

## Selecting checks

The [verification skill](../.agents/skills/kc-verification/SKILL.md) routes
changes to checks; the Makefile defines the actual commands. Start focused and
expand according to changed contracts, not a fixed ritual on every edit.

| Command | Purpose |
| --- | --- |
| `make test-unit` | Tests with envtest explicitly disabled |
| `make test-integration` | Tests with envtest required; unavailable assets fail |
| `make test-race` | Race detector for the selected package scope |
| `make test-tui` | UI model/rendering, table and headless-driver unit tests; no application E2E claim |
| `make test-headless` | Actual kc/driver session against isolated envtest; startup, resize, help and clean exit |
| `make test-fuzz` | Bounded fuzz checks of existing targets |
| `make check-agents` | Agent-file structure, references and budget checks |
| `make build` / `make lint` | Build and the repository's pinned lint checks |

`PACKAGES` narrows unit, integration and race targets, for example
`make test-unit PACKAGES=./internal/ui/...`. `GO_TEST_FLAGS` can select a known
test with `-run`; `TEST_TIMEOUT` bounds a package test run (default `5m`).
`FUZZ_TIME` bounds each fuzz target (default `10s`). Do not mistake a per-package timeout
for a limit on the entire command, or choose a `-run` pattern that matches no
tests. Review the actual output, including skipped tests.

For shared interfaces, check affected reverse importers. Module, global config,
embedded fixture and validation-tooling changes can require broader coverage
than the edited package. Run build/lint and required broader checks on final
code. Repeated integration runs, `-count` and fuzzing need a concrete target;
do not run them indefinitely or run expensive suites concurrently.

## Select tests for a change

From the repository root:

```sh
python3 scripts/test-changed.py --plan
python3 scripts/test-changed.py --mode unit
python3 scripts/test-changed.py --base origin/main --mode integration --timeout 5m
python3 scripts/test-changed.py --mode race
```

`--plan` prints reasons and the command without running tests. The base defaults
to `origin/main`; selection includes committed, staged, unstaged and untracked
changes against its merge base with `HEAD`. Renames retain both old and new paths.
For code changes, one `go list -json ./...` supplies the current package graph.
Selection includes reverse normal/test imports, package testdata and embedded
inputs. New or removed embedded files use conservative pattern prefixes.

Shared build/config/tooling changes, unknown non-documentation inputs, missing
packages or unavailable package metadata select `./...`. Documentation-only
changes can return without a Go toolchain; embedded Markdown still counts as
code input. Unit/race modes disable envtest, integration requires it, and each
mode clears the conflicting inherited switch. `--timeout` is per package.

This is a local optimization for the current Go build configuration, not a
replacement for required CI, build/lint, platform checks or agent-file checks.
Inspect the plan when changing shared contracts or fixtures.

## Envtest constraints

Follow [CONTRIBUTING.md](../CONTRIBUTING.md) for the supported toolchain and
asset setup. The source of truth for versions is `go.mod` and the CI workflow.
The modes are defined by `internal/testing/kcenvtest`:

| Mode | Behavior |
| --- | --- |
| `KC_REQUIRE_ENVTEST=1` | Control-plane startup failure fails the test/package |
| `KC_SKIP_ENVTEST=1` | Envtest is not started and dependent tests skip |
| Neither | Startup failure may skip integration tests |

Never enable both switches. Auto/skipped mode cannot substantiate an
integration-pass claim. Do not turn required mode off to obtain green output.

Use `kcenvtest.Main` and `kcenvtest.Require` for a shared package control plane.
Use `kcenvtest.Start` or `StartEnv` only when a test needs isolation of
cluster-wide state such as CRDs. Do not call `envtest.Environment.Start`
directly. On a shared server, use `kctesting.UniqueName` for namespaces, users
and cluster-scoped fixtures. Run clusters through
`kctesting.RunInBackground(t, "cluster", cl.Start)` so caches stop before the
API server. After grants, use `kctesting.WaitForAccess` for the verbs tested;
RBAC authorizer and cache propagation are asynchronous.

Envtest provides an API server and etcd, not kubelets, scheduler or controllers.
It can test API, cache, watch and RBAC behavior, but does not prove running-pod
exec, streaming logs, port forwarding, scheduling or live metrics. Test those
boundaries with controlled fakes, check them live on a
[throwaway kind cluster](#throwaway-kind-clusters), and report what neither
covered.

The shared helper advertises loopback explicitly, so a test container does not
need a default network route. It disables the API server's bootstrap
`default/kubernetes` Endpoints/EndpointSlice reconciler, which rejects loopback;
the API and creation of the Kubernetes Service remain available. Tests of
endpoint resources use explicit fixtures. Caller-supplied flags take precedence.

References: [required envtest](adr/0060-require-envtest-in-ci.md),
[envtest lifecycle and load](adr/0244-envtest-under-load.md).

## Headless TUI checks

Prefer deterministic state/view tests for focus, key routing, resize, clipping
and asynchronous results. `make test-tui` runs these and driver unit tests;
it does not start kc in a terminal or verify a complete user journey.

For a justified application-level check, an isolated harness must start envtest,
write its kubeconfig to a temporary file, isolate kc's configuration/history,
and pass that kubeconfig explicitly to the child. Run the driver around the
built binary: `bubbleheadless -cols 120 -rows 40 -- kc --kubeconfig <fixture>`.
Here `<fixture>` is the harness-created envtest file, never an existing user
kubeconfig. Building the binaries first avoids rebuilding between observations.

The repository harness is `make test-headless`. It builds both binaries once,
uses a temporary home and envtest kubeconfig, observes startup at 120x30 and
resize to 100x24, opens/closes help, and checks the actual kc exit status. It
requires envtest assets; regular package tests skip it unless explicitly enabled.
Extend this fixture for a relevant terminal regression instead of introducing
another setup path. Its passing smoke journey does not cover all application
features or prove visual quality without inspecting the affected frames.

The driver accepts these commands on stdin:

| Command | Meaning |
| --- | --- |
| `key <token>` / `text <value>` | Send a key token such as `tab`, `enter`, `esc` or text |
| `mouse <button> <x> <y> <press\|release\|motion>` | Send a mouse event |
| `resize <cols> <rows>` | Resize the terminal and signal the child |
| `screen plain` / `screen ansi` | Capture the current framebuffer |
| `snapshot <absolute-path.png> [color]` | Save a monochrome or color frame |
| `images` | Inspect received Kitty graphics and their display status |
| `caption <text>` / `caption-at top` | Set an optional snapshot caption; bare `caption` clears it |
| `wait` | Wait for the child to exit, not for a stable frame |
| `exit` | Stop the child and driver |

An external harness must poll for the expected observable state with a bounded
deadline. An immediate screenshot or a fixed sleep is not a readiness check.
Use the failing viewport size and one representative normal size; inspect
Unicode width, focus and truncation only where affected. Persist a representative
frame for a visual change, alongside the behavioral assertions.

The driver hides the outer terminal's identification variables. In `auto`
graphics mode kc therefore uses text; a fixture configured with
`charts.graphics: kitty` exercises image output. Color snapshots include box
drawing and Kitty graphics; see
[headless Kitty support](adr/0352-bubbleheadless-kitty-graphics.md).

A journey that needs running pods goes to a
[throwaway kind cluster](#throwaway-kind-clusters), driven the same way.

## Throwaway kind clusters

A kind cluster created for the task and deleted afterwards is not a real
cluster in the sense of AGENTS.md: nothing runs on it but what the task put
there, and its kubeconfig exists only in a temporary directory. Use one to check
what envtest cannot show: running pods, logs, exec and attach, port forwards,
metrics, controllers acting on objects (rollouts, ReplicaSets, Jobs), node
shells and drains. The screenshot catalog and the demo GIF are made on one too.
Tests stay on envtest and fakes, which CI runs; a live check is extra evidence
for the PR, not a replacement for a regression test.

`screenshots/capture.sh` creates the cluster and drives kc on it, one command
per call; its header lists every command and variable:

```sh
screenshots/capture.sh up                       # create the cluster
screenshots/capture.sh seed                     # optional: demo objects, metrics-server, a Helm release
screenshots/capture.sh run kubectl get pods -A  # kubectl or helm on this cluster only
screenshots/capture.sh start                    # kc in the driver, in the background
screenshots/capture.sh text 'cd deploy'
screenshots/capture.sh keys enter               # driver key tokens: f3, esc, ctrl+x, ...
screenshots/capture.sh wait report 30           # bounded wait for text on the screen
screenshots/capture.sh screen                   # the frame as plain text
screenshots/capture.sh snap /tmp/frame.png      # a color frame for the PR
screenshots/capture.sh stop
screenshots/capture.sh down                     # delete the cluster and the work directory
```

- Create the cluster through the script, so that kc and kubectl run with a
  temporary `HOME` and a kubeconfig that holds only this cluster, and every
  command first checks the context (`kind-<cluster>`) and the server
  (`127.0.0.1:<port>`). Never merge that kubeconfig into the user's, pass a real
  context name, or use a kind cluster you did not create for the task.
- Docker can run locally or on another host (`KC_SHOTS_SSH`, reached through an
  ssh tunnel); use the Docker host the user prefers. Remove only what you
  created, never with blanket prunes.
- Give each concurrent session its own `KC_SHOTS_CLUSTER`, `KC_SHOTS_PORT`,
  `KC_SHOTS_WORK` and `KC_SHOTS_LOCAL`.
- Run `down` when done, also after a failure, and check on the Docker host that
  the cluster is gone (`kind get clusters`). Report anything left behind.
- In the PR, say what was checked live, at which revision, and what was not.

## Evaluate agent changes

### Initial routing review

On 2026-11-28, a separate fresh-context reviewer routed eight prompts using
the proposed instructions: a README typo, parser error, stale cross-context
response, narrow Unicode layout, failed informer shutdown, deleted embed
selection, comment formatting, and RBAC propagation. Prose/mechanical prompts
loaded no skills; the known-cause parser fix did not load debugging guidance;
the two intermittent/failed-fix prompts did. The review exposed an overly broad
completion trigger and generic Go overlap with layout-only work; both triggers
were narrowed. This was a qualitative routing check, not an implementation
benchmark or evidence of a measured token/cost reduction.

### Compare accepted implementations

Treat skill effectiveness as a hypothesis. A structural check proves that a
skill can be read, not that it improves implementations or reduces their cost.
Compare the current baseline and proposed instructions on the same frozen
starting revisions, model/settings, task prompts and acceptance checks. Use
independent worktrees and fresh sessions; keep the expected fix hidden from
the worker and assess the result independently.

Use a small representative task set; replace examples with frozen reproductions
before measuring. These are evaluation scenarios, not completed results:

| Scenario | Expected routing and acceptance evidence |
| --- | --- |
| Documentation typo | No domain/debugging workflow; no unnecessary Go suite |
| Local Go behavior fix | Go + verification; meaningful regression and focused checks |
| Resize/Unicode layout defect | TUI + verification; normal/narrow view assertions |
| Late response after panel switch | TUI + Kubernetes + verification; origin/cancellation regression |
| Cache mutation or RBAC propagation | Kubernetes + verification; real envtest semantics |
| Intermittent goroutine shutdown | Go + conditional debugging; deterministic synchronization and targeted race check |
| Shared interface/config change | Relevant skills; reverse-importer or broad fallback coverage |
| Agent-file/tooling change | Structural checks plus positive/negative routing prompts |

Record each run using this template; leave unavailable metrics blank:

| Task / base SHA / instruction SHA | Model and settings | Input / cached input / output tokens | Wall time | Tool calls / retries | Checks and skips | Accepted? / required rework |
| --- | --- | --- | --- | --- | --- | --- |
| _One row per independent run_ | | | | | | |

Count all worker/reviewer contexts and repair attempts, not only the final
response. Compare acceptance rate, defects missed and rework first, then total
tokens and elapsed time per accepted task. Keep cached-input metrics separate;
token counts do not alone determine price. Repeat ambiguous cases to separate
instruction effects from model variation. Do not claim a percentage improvement
without recorded comparable runs. Stop when the sample answers the decision;
this evaluation is not an unlimited CI or agent loop.

## Sources and licenses

The kc skills are project-specific instructions. External collections informed
their design; no upstream plugin, session hook, global skill installation or
automatic update mechanism is included.

Superpowers v7.0.2 was reviewed at commit
`22f0b463e7a5e579904dd6211a520321035526f7`:
[source tree](https://github.com/obra/superpowers/tree/22f0b463e7a5e579904dd6211a520321035526f7/skills).
The verification/debugging workflow adapts its root-cause investigation,
evidence-before-claims and focused-review concepts. Its MIT notice is retained
in [.agents/licenses/superpowers-MIT.txt](../.agents/licenses/superpowers-MIT.txt).
The local workflow narrows triggers, defaults to one implementer, reuses valid
test evidence and avoids mandatory design/plan approvals and cascading skills.
It is not an unmodified installation of Superpowers.

Reviewed references, with no files copied into kc:

- [JetBrains/go-modern-guidelines: use-modern-go](https://github.com/JetBrains/go-modern-guidelines/tree/155dc7ca10da5e1f6c841503086957b1b37f5815/plugin/skills/use-modern-go),
  Apache-2.0; reviewed commit `155dc7ca10da5e1f6c841503086957b1b37f5815`,
  `SKILL.md` blob `063d4a6438922e42170dd1208af5adf360b7670d`. This is an optional Go reference;
  kc's `go.mod`, existing contracts and pinned linters govern actual changes.
- [gfargo/tui-design-skill](https://github.com/gfargo/tui-design-skill/tree/0a0ce996e00fc2a5db7f1f85495257381cc90df9),
  MIT; reviewed `plugins/tui-design/skills/tui-design/SKILL.md` and
  `references/ecosystem-go.md` at commit
  `0a0ce996e00fc2a5db7f1f85495257381cc90df9`. Its ecosystem examples are not
  authoritative for kc's pinned Bubble Tea v2 API.

Update provenance when deliberately importing an upstream revision. Review its
triggers, references, commands and license before copying; never update agent
instructions automatically from a moving branch during an implementation task.
