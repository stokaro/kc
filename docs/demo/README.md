# Demo recording

`record.sh` produces `docs/demo.gif`, the animation at the top of the README. It creates a throwaway kind cluster named `kc-demo`, seeds it with the objects in `seed.yaml`, drives kc through `cmd/bubbleheadless`, renders the frames into a GIF and deletes the cluster again. Rerun it when the UI changes.

The script never touches your own clusters. kc and kubectl run with a temporary `HOME` and a kubeconfig that holds only the kind cluster, and every kubectl call first checks that the current context is `kind-kc-demo` and that the server is `https://127.0.0.1:<port>`.

## Prerequisites

- Go (the version in `go.mod`), to build kc and the driver.
- Docker and [kind](https://kind.sigs.k8s.io/), on this machine or on a Linux host you reach over ssh.
- kubectl and ffmpeg on this machine.
- Network access for the node image, the public `nginx` and `busybox` images, and the metrics-server manifest and image.

## Run

From the repository root:

```bash
docs/demo/record.sh
```

This runs every step and removes the cluster at the end, also when a step fails. To run kind on another Docker host, give its ssh destination; the API server is then reached through an ssh tunnel on `127.0.0.1`:

```bash
KC_DEMO_SSH=user@docker-host docs/demo/record.sh
# without kind on that host's PATH, upload a Linux build:
KC_DEMO_SSH=user@docker-host KC_DEMO_REMOTE_KIND=/path/to/linux/kind docs/demo/record.sh
```

The steps also run one by one, which helps when you change a scene: `up` (create the cluster), `seed`, `record` (frames only), `gif`, `down` (delete the cluster and the work directory). Pass the same settings to every step. The scenes scale a Deployment and drain a node; `record` sets both back first, so it can run again on the same cluster.

Settings, all optional:

| Variable | Default | Meaning |
|---|---|---|
| `KC_DEMO_SSH` | empty (local Docker) | ssh destination of the Docker host for kind |
| `KC_DEMO_REMOTE_KIND` | empty | Linux kind binary to upload when the host has none |
| `KC_DEMO_PORT` | `39158` | API server port on `127.0.0.1` |
| `KC_DEMO_WORK` | `$TMPDIR/kc-demo` | work directory: kubeconfig, temporary `HOME`, binaries, frames |
| `KC_DEMO_LOCAL` | `/tmp/kc-demo-downloads` | directory kc starts in; the copy scene writes there, and the GIF shows its path |
| `KC_DEMO_OUT` | `docs/demo.gif` | output file |

## What it records

The scenes are shell functions near the end of `record.sh` and run in the order of `SCENES`. Each scene starts where the one before it ends. `KC_DEMO_SCENES="intro search"` records only some of them, which is quicker when you change one. Every scene sets a caption that the driver draws over its frames:

| Scene | Captions |
|---|---|
| `intro` | kc: a two-panel Kubernetes TUI in the style of Midnight Commander |
| `search` | Quick search (C-s): type the start of a name, Enter opens the row; The Info panel follows the cursor: usage, pods, top pods |
| `overview` | Cluster overview (M-i): what needs attention in this namespace; Info explains the finding under the cursor; Enter lists the objects behind a finding |
| `diagnosis` | /diagnosis explains why a Deployment is broken; ... and why a Service has no endpoints |
| `menu` | F9 opens the menu bar, as in mc; each entry shows its key; The Right menu switches the right panel from Info to a list |
| `drain` | Node actions (M-n): drain a node with progress, then uncordon; Uncordon makes the node schedulable again; the pods come back |
| `scale` | /timeline records every change kc observes on an object; Scale... asks first; the pods on the right follow live; The timeline shows the scale-up, newest first; F3 on an entry diffs it against the revision before |
| `yaml` | F3 views any object; this is the YAML of the Deployment |
| `compare` | C-x C-d compares the objects under the two cursors |
| `logs` | Merged logs of every pod of a Deployment, JSON lines formatted; M-g filters by JSON fields: level=error,warn |
| `files` | Container files: browse /root of a running container; M-l opens a local directory; F5 copies out of the container |
| `hotlist` | Hotlist (C-\\): saved places and views with their filter and sort; Another saved view: pods in batch that restart, most restarts first |
| `terminal` | C-o: the integrated terminal follows the panel's context and namespace; C-o again returns to the panels |

`seed.yaml` gives the overview and `/diagnosis` something to find: `shop/checkout` pulls an image that does not exist, `shop/payments` is a Service without matching pods, and `batch/report` exits with an error and restarts. `seed` also installs metrics-server (with `--kubelet-insecure-tls`, which kind needs, and on the control-plane node so the drain of the worker does not wait for it), because the Info panel and the overview show usage. `record` writes the hotlist entries of the `hotlist` scene into the temporary `HOME` before it starts kc.

Each step waits for the screen to show what it needs (`screen plain` through the driver) before it takes a frame, so a slow cluster changes the timing, not the content.

## Captions and pacing

`caption <text>` in the driver draws a dark box with a yellow frame and large white text over the following `snapshot <file> color` frames, centered over the shell line and the function-key bar, where it hides the least. A bare `caption` clears it; `caption-at top` moves it to the top rows.

The number after `snap` is how long a frame stays in the GIF. The scenes use named values so the pacing stays even:

| Name | Seconds | Used for |
|---|---|---|
| `FIRST` | 3 | the first frame with a new caption |
| `READ` | 3 | a frame with text to read |
| `STEP` | 1.8 | an ordinary step |
| `TYPE` | 1 | a frame that shows typed text |
| `ANIM` | 0.8 | one step of a live change: the scale-up, the drain progress |

The GIF runs about 2 minutes 20 seconds. `snapshot <file> color` renders a frame with its colors, Go Mono and drawn box lines; ffmpeg builds one shared 64-color palette without dithering, which keeps the file near 1.4 MB.
