# 0244. Envtest under load

- Status: Accepted
- Date: 2026-11-23
- PR: #244
- Issue: #232

## Context

Envtest tests passed when their package ran alone and failed in a full
`go test ./...` on a loaded machine. Measured on a laptop with a load
average above 50 and two full suites running side by side:

- **Stop timeouts.** With an informer still watching, `kube-apiserver` 1.37
  waits for open watches until its own 60s shutdown timeout. envtest sends
  SIGTERM, waits 20s, kills the process and returns
  "timeout waiting for process kube-apiserver to stop". A probe measured
  60.1s for a stop with live informers, 0.07s once the informers had
  stopped. Tests started clusters with `go cl.Start(ctx)` and nothing waited
  for the informers to stop before the control plane did, and
  `cluster.Cluster.Start` returned as soon as one of its two caches stopped.
- **Start timeouts.** About twenty control planes started at once: one per
  package from `kcenvtest.Main`, plus five parallel per-test ones in
  `internal/navigation`, six in `internal/models`, three in
  `internal/cluster`. envtest's 20s start bound per attempt ran out, and its
  fixed 5s wait for the `default` namespace after the server is ready failed
  ("default namespace didn't register within deadline"). envtest left etcd
  and kube-apiserver running after that error.
- **Fixed names.** Tests that share a control plane created namespaces,
  users and other cluster-wide objects with fixed names. envtest runs no
  namespace controller, so a deleted namespace stays `Terminating`.
  `-count=3` failed 37 tests in `internal/models`, `internal/ui` and
  `internal/tablecache`, among them `TestHelmFoldersEnvtest`.
- `internal/explain` and one test in `internal/cluster` called
  `envtest.Environment.Start` directly and skipped on any startup error, so
  CI could skip them silently (ADR 0060 forbids this).

## Decision

1. **`kcenvtest` tunes every control plane** (`kcenvtest.Tune`, applied by
   `Main`, `Start` and the new `StartEnv`):
   - `--shutdown-watch-termination-grace-period=1s` on kube-apiserver, so a
     watch left open cannot stretch a stop past the stop timeout. The same
     probe stops in 0.16s with live informers.
   - Start timeout 90s and stop timeout 60s per process, unless
     `KUBEBUILDER_CONTROLPLANE_START_TIMEOUT` or `..._STOP_TIMEOUT` is set.
     Both are upper bounds; a healthy start or stop returns as soon as the
     process is ready or gone.
   - A failed start stops whatever envtest left running. A start that failed
     only on the `default` namespace wait is retried with a fresh control
     plane, up to three attempts. The error text is matched against the
     message of the pinned controller-runtime (v0.25.3).
2. **At most two per-test control planes per test binary.** `Start` and
   `StartEnv` take a slot before starting and give it back after the stop.
   Parallel tests that need their own control plane wait for a slot instead
   of booting all at once. A test must not call `Start` more than twice.
3. **One shared control plane per package is the default.**
   `internal/navigation` moves to `kcenvtest.Main`. In `internal/models`,
   tests that only read namespaced objects move to the shared control plane.
   A test keeps its own only when it changes cluster-wide state other tests
   must not see, such as installing a CRD or checking that a kind is not
   served.
4. **Unique names on shared control planes.** Namespaces, impersonated users
   and nodes come from `kctesting.UniqueName(base)`, which appends five
   random characters. Every package passes with `-count=3`.
5. **Clusters stop before the control plane.** Tests start clusters with
   `kctesting.RunInBackground(t, name, cl.Start)`. Its cleanup cancels the
   context and waits until `Start` returns. `cluster.Cluster.Start` now
   returns after a cancel only when both caches have stopped. When one cache
   fails while the context is still live, it returns that error at once, as
   before.
6. `internal/explain` and `internal/cluster` start their control planes
   through `kcenvtest`.

## Alternatives considered

- **Raise the timeouts only.** A 60s stop timeout would turn every stop with
  a live informer into a 60s wait, and does nothing for the fixed 5s
  namespace wait or for the number of control planes.
- **Run envtest packages with `-p 1` or serialize all envtest tests.** It
  removes the load, but CI time grows with the number of packages, and a
  developer's `go test ./...` keeps the old failures.
- **A semaphore across test binaries** (a lock file). It would bound the
  whole `go test ./...`, not one binary, but adds file locking and stale-lock
  handling to every test run. The per-binary cap plus shared control planes
  brought the peak from about twenty to about ten.
- **Delete namespaces in cleanup and keep fixed names.** envtest has no
  namespace controller; deleted namespaces never go away.
- **`GenerateName` on the server.** Equivalent for namespaces, but users
  and RBAC subjects need names before any object exists, and one helper
  covers both.

## Consequences

- A package that needs its own control plane per test runs those tests at
  most two at a time; `internal/models` takes a few seconds longer when run
  alone.
- New envtest tests name shared objects with `kctesting.UniqueName` and
  start clusters with `kctesting.RunInBackground`. `AGENTS.md` says so.
- The 5s namespace wait is still inside envtest. If a future
  controller-runtime changes its message, the retry stops matching and the
  start fails as before, with the same message.
