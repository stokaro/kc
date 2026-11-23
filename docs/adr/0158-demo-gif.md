# 0158. Demo GIF recorded by a script

- Status: Accepted
- Date: 2026-11-22
- Issue: #158

## Context

The README showed one static screenshot. The issue asks for a short GIF of kc's main features, recorded with `cmd/bubbleheadless` against a throwaway kind cluster, with the script in the repository so the recording can be repeated when the UI changes. It left open how frames are rendered, how the GIF is built, which scenes go in, and how the script reaches a cluster on a machine without a local Docker.

AGENTS.md forbids running kc against any real cluster, including local ones that look disposable. The demo needs pods, logs, a container filesystem and a node to drain, which envtest cannot provide.

## Decision

1. `docs/demo/record.sh` creates its own kind cluster `kc-demo`, seeds it from `docs/demo/seed.yaml`, records, builds the GIF and deletes the cluster, also on failure. kc and kubectl run with a temporary `HOME` and a kubeconfig that holds only that cluster; every kubectl call first checks the context name and that the server is `https://127.0.0.1:<port>`. This is the one place where kc runs against a cluster other than envtest, and only against a cluster the script created.
2. kind runs on local Docker by default. With `KC_DEMO_SSH`, it runs on a remote Linux Docker host with `apiServerAddress: 127.0.0.1` and a fixed port, reached through `ssh -L`. kind keeps its kubeconfig in a file on that host and never edits the host's `~/.kube/config`.
3. Frames come from a new `snapshot <file> color` command in `cmd/bubbleheadless`. It renders the emulator's cells with their colors in Go Mono (from `golang.org/x/image`, already a dependency) and draws box-drawing and block characters as shapes so frame lines join. Glyphs Go Mono lacks (emoji) are left blank. The plain `snapshot <file>` is unchanged.
4. ffmpeg assembles the GIF with the concat demuxer, one GIF frame per snapshot with its own delay (`-fps_mode vfr`), and one 64-color palette without dithering. The result is 1080x612, about 41 seconds and 0.7 MB.
5. Scenes: both panels and quick search; scaling a Deployment with the live pod list; YAML and merged JSON logs; copying a file out of a container into a local directory; draining and uncordoning a node; the integrated terminal. Each step waits for text on the screen (`screen plain`) before it acts or takes a frame; there are no fixed waits for the cluster.
6. The local directory of the copy scene is `/tmp/kc-demo-downloads` (`KC_DEMO_LOCAL`), because the GIF shows its path and a temporary directory would show a long, user-specific one.

## Alternatives considered

- The existing plain snapshot: monochrome, and the 7x13 basic font has no box-drawing glyphs, so panels lose their frames.
- `screen ansi` to HTML rendered by headless Chrome (how the earlier screenshot was made): needs Chrome and per-frame browser runs; drawing the cells in Go is faster and has no extra tool.
- A Go GIF encoder in the driver: ffmpeg's palette and frame-difference encoding give a smaller file for little code, and ffmpeg is a common tool. The script is maintainer tooling, not part of kc.
- Fixed sleeps between keys: break on a slower cluster or machine. Waiting for screen text costs a few helper functions.
- Cordon only: shorter, but the drain progress dialog is the more useful thing to show. The busybox pods trap `SIGTERM` and set a 2 second grace period so the drain does not wait 30 seconds.

## Consequences

- Rerunning the demo needs Docker, kind, kubectl, ffmpeg and network access for images. The script, not CI, produces the GIF.
- The GIF goes stale when the UI changes; rerun `docs/demo/record.sh`. A scene whose screen text changes fails with the text it waited for and the screen it saw.
- `cmd/bubbleheadless` imports `github.com/charmbracelet/ultraviolet` directly now (it was already an indirect dependency through bubbleterm).
- The help viewer (`F1`) already drops image lines from the README; its explicit check for the old screenshot path now names `docs/demo.gif`.

## Update 2026-11-23: captions and more scenes

The first GIF moved too fast to follow and showed no text that said what each step was. The demo now has captions and covers more of kc:

- `cmd/bubbleheadless` has `caption <text>` and `caption-at <top|bottom>`. Color snapshots draw the caption as a dark box with a yellow frame and Go Bold text, centered over the bottom two rows (the shell line and the function-key bar), where it hides the least. The caption is drawn on the PNG, not sent to kc, so the recorded UI is unchanged.
- Frames stay at least 0.8 seconds (one step of a live change), most 1.8 to 3 seconds, and the first frame with a new caption 3 seconds. Decision 4's numbers are now about 2 minutes 25 seconds and 1.4 MB.
- Scenes added to decision 5: the overview and `/attention`, `/diagnosis` of a broken Deployment and a Service without endpoints, context help (`F1`) on a folder and in a dialog, the menu bar, `/timeline` with its diff, comparing two objects, the log filter, and the hotlist. `seed.yaml` adds the broken objects they need, and `seed` installs metrics-server for the usage the Info panel shows.
