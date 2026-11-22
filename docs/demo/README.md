# Demo recording

`record.sh` produces `docs/demo.gif`, the animation at the top of the README. It creates a throwaway kind cluster named `kc-demo`, seeds it with the objects in `seed.yaml`, drives kc through `cmd/bubbleheadless`, renders the frames into a GIF and deletes the cluster again. Rerun it when the UI changes.

The script never touches your own clusters. kc and kubectl run with a temporary `HOME` and a kubeconfig that holds only the kind cluster, and every kubectl call first checks that the current context is `kind-kc-demo` and that the server is `https://127.0.0.1:<port>`.

## Prerequisites

- Go (the version in `go.mod`), to build kc and the driver.
- Docker and [kind](https://kind.sigs.k8s.io/), on this machine or on a Linux host you reach over ssh.
- kubectl and ffmpeg on this machine.
- Network access for the node image and the public `nginx` and `busybox` images.

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

The scenes are shell functions at the end of `record.sh`:

1. Both panels at the root; quick search (`C-s`) into `/namespaces/shop/deployments` on the left and the namespace's pods on the right.
2. `Scale...` on the `web` Deployment, 2 to 4 replicas, with the confirmation; the pod list on the right picks up the new pods live.
3. `F3` on the Deployment for its YAML, then the merged logs of the `api` Deployment, whose JSON lines kc formats.
4. The `/root` folder of the nginx container, a local directory (`M-l`) in the other panel, and `F5` to copy `nginx.conf` out of the container.
5. `cd` on the shell line, then `M-n` on the worker node: drain with its options, confirmation and progress, then uncordon.
6. The integrated terminal (`C-o`) running kubectl.

Each step waits for the screen to show what it needs (`screen plain` through the driver) before it takes a frame, so a slow cluster changes the timing, not the content. `snapshot <file> color` in the driver renders a frame with its colors, Go Mono and drawn box lines. The number after `snap` is how long a frame stays in the GIF; ffmpeg builds one shared 64-color palette without dithering, which keeps the file small.
