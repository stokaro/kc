# 0060. Require envtest in CI

- Status: Accepted
- Date: 2026-11-22
- Issue: #60

## Context

The envtest suites in `internal/models`, `internal/ui` and `internal/tablecache`
started a control plane in `TestMain` and ignored a startup error. Every test
then skipped on a nil config. Other tests (`internal/cluster`, some in
`internal/models`) started their own control plane and skipped on some error
strings and failed on others; `internal/navigation` always failed. A CI run
with missing or broken binaries could therefore report success without running
the integration coverage. The workflow also hid a failing `setup-envtest`
behind `eval "$(...)"`.

The issue leaves open the names of the switches, whether CI is detected
implicitly, and what a developer gets with no switch at all.

## Decision

1. **Two switches, read by one helper package.** `internal/testing/kcenvtest`
   reads `KC_REQUIRE_ENVTEST` and `KC_SKIP_ENVTEST`. A value is parsed with
   `strconv.ParseBool`; an empty value counts as unset; an unparsable value or
   both switches set is an error that fails the package (exit 2) or the test.

   | Mode | Switch | Control plane cannot start |
   | --- | --- | --- |
   | required | `KC_REQUIRE_ENVTEST=1` | package or test fails |
   | skipped | `KC_SKIP_ENVTEST=1` | not started; envtest tests skip |
   | auto | neither | envtest tests skip, with the reason |

2. **Auto is the default.** A developer without binaries keeps a green
   `go test ./...`. This is the old behavior for most packages, and now also
   for `internal/navigation`, which used to fail.

3. **CI opts in explicitly.** The linux-amd64 test step sets
   `KC_REQUIRE_ENVTEST=1`. `CI=true` is not read.

4. **One entry point per shape.** A package with a shared control plane calls
   `os.Exit(kcenvtest.Main(m, &testCfg))` in `TestMain` and guards each test
   with `kcenvtest.Require(t, testCfg)`. A test that needs its own control plane
   calls `cfg := kcenvtest.Start(t)`, which also stops it in `t.Cleanup`. In
   required mode `Main` returns 1 before running any test, and `Require` fails
   instead of skipping if a package forgot to call `Main`.

5. **Diagnosable errors.** Before starting, the helper resolves `etcd` and
   `kube-apiserver` the way envtest does (`TEST_ASSET_*`, then
   `KUBEBUILDER_ASSETS`, then `/usr/local/kubebuilder/bin`) and reports each
   path as ok, missing or not executable, plus the `setup-envtest` command when
   `KUBEBUILDER_ASSETS` is unset. A control-plane failure carries the same
   report and the envtest error.

6. **Setup failure stops the job.** The workflow assigns
   `assets="$(setup-envtest use -p path ...)"`; under the runner's `bash -e`
   a failing assignment ends the step.

## Alternatives considered

- **Treat `CI=true` as required.** It would also catch a future CI job that
  forgets the switch, but `CI` is set by many systems, including packagers who
  run tests in a sandbox without envtest. The issue asks for an explicit mode.
- **Required by default, opt out locally.** Safest, but every contributor
  without `setup-envtest` would see a red suite on first checkout, and the
  project documents envtest as optional for local work.
- **Fail on any startup error and drop skipping.** Same cost as above, and no
  way to run the quick tests alone.
- **A build tag for envtest tests.** Splits the suites at compile time, so a
  missing tag in CI silently removes them again; it also needs every envtest
  file to be retagged.
- **Check binaries in the workflow with shell.** The Go helper already has to
  resolve them to produce a useful error, and it then covers local runs too.

## Consequences

- A green linux-amd64 CI job means every envtest test ran. Skips in that job
  can only come from tests that skip for other reasons.
- New envtest tests must go through `kcenvtest`; a direct
  `envtest.Environment.Start` reintroduces the silent path. `AGENTS.md` says
  so; no check enforces it yet.
- The `1.37.x` hint in error messages duplicates `ENVTEST_K8S_VERSION` in the
  workflow and has to move with it.
- The workflow still installs `setup-envtest@latest`. Pinning it is separate
  work.
