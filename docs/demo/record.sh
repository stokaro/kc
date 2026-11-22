#!/usr/bin/env bash
# Records docs/demo.gif: creates a throwaway kind cluster, seeds demo objects,
# drives kc through cmd/bubbleheadless, renders the frames to a GIF and
# deletes the cluster again. See docs/demo/README.md.
#
# Usage: docs/demo/record.sh [all|up|seed|record|gif|down]
#
# Environment:
#   KC_DEMO_SSH          optional ssh destination of a Linux Docker host; kind then
#                        runs there and the API server is reached through an ssh tunnel
#   KC_DEMO_REMOTE_KIND  local path of a Linux kind binary to upload when the
#                        remote host has no kind on PATH
#   KC_DEMO_PORT         API server port on 127.0.0.1 (default 39158)
#   KC_DEMO_WORK         work directory (default ${TMPDIR:-/tmp}/kc-demo)
#   KC_DEMO_LOCAL        local directory kc starts in; the copy scene writes there
#                        and the GIF shows its path (default /tmp/kc-demo-downloads)
#   KC_DEMO_OUT          output GIF (default docs/demo.gif)
set -euo pipefail

CLUSTER=kc-demo
CONTEXT=kind-$CLUSTER
PORT=${KC_DEMO_PORT:-39158}
REPO=$(cd "$(dirname "$0")/../.." && pwd)
WORK=${KC_DEMO_WORK:-${TMPDIR:-/tmp}/kc-demo}
WORK=${WORK%/}
OUT=${KC_DEMO_OUT:-$REPO/docs/demo.gif}
LOCAL=${KC_DEMO_LOCAL:-/tmp/kc-demo-downloads}
SSH_DEST=${KC_DEMO_SSH:-}
REMOTE_DIR=kc-demo-work
COLS=120
ROWS=34

mkdir -p "$WORK/home"
KCFG=$WORK/kubeconfig

log() { printf '[demo] %s\n' "$*" >&2; }
die() { log "error: $*"; exit 1; }

remote() { ssh -o BatchMode=yes "$SSH_DEST" "$@"; }

# kk runs a command with the throwaway HOME and KUBECONFIG, after checking that
# the kubeconfig points at the demo cluster on the local port and nothing else.
kk() {
	[ -s "$KCFG" ] || die "no kubeconfig at $KCFG; run '$0 up' first"
	local ctx server
	ctx=$(HOME=$WORK/home KUBECONFIG=$KCFG kubectl config current-context)
	server=$(HOME=$WORK/home KUBECONFIG=$KCFG kubectl config view --minify -o 'jsonpath={.clusters[0].cluster.server}')
	[ "$ctx" = "$CONTEXT" ] || die "current context is $ctx, want $CONTEXT"
	[ "$server" = "https://127.0.0.1:$PORT" ] || die "server is $server, want https://127.0.0.1:$PORT"
	HOME=$WORK/home KUBECONFIG=$KCFG TZ=UTC "$@"
}

kind_config() {
	cat <<EOF
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
networking:
  apiServerAddress: 127.0.0.1
  apiServerPort: $PORT
nodes:
  - role: control-plane
  - role: worker
EOF
}

tunnel_pidfile=$WORK/tunnel.pid

start_tunnel() {
	[ -n "$SSH_DEST" ] || return 0
	if [ -f "$tunnel_pidfile" ] && kill -0 "$(cat "$tunnel_pidfile")" 2>/dev/null; then
		return 0
	fi
	ssh -o BatchMode=yes -o ExitOnForwardFailure=yes -N -L "$PORT:127.0.0.1:$PORT" "$SSH_DEST" &
	echo $! >"$tunnel_pidfile"
	sleep 2
	kill -0 "$(cat "$tunnel_pidfile")" || die "ssh tunnel did not start"
}

stop_tunnel() {
	if [ -f "$tunnel_pidfile" ]; then
		kill "$(cat "$tunnel_pidfile")" 2>/dev/null || true
		rm -f "$tunnel_pidfile"
	fi
}

cmd_up() {
	if [ -z "$SSH_DEST" ]; then
		kind_config >"$WORK/kind.yaml"
		HOME=$WORK/home kind create cluster --name "$CLUSTER" --config "$WORK/kind.yaml" --kubeconfig "$KCFG" --wait 120s
	else
		remote "mkdir -p $REMOTE_DIR"
		local rkind=kind
		if ! remote "command -v kind" >/dev/null; then
			[ -n "${KC_DEMO_REMOTE_KIND:-}" ] || die "no kind on $SSH_DEST; set KC_DEMO_REMOTE_KIND"
			remote "cat > $REMOTE_DIR/kind && chmod +x $REMOTE_DIR/kind" <"$KC_DEMO_REMOTE_KIND"
			rkind=$REMOTE_DIR/kind
		fi
		echo "$rkind" >"$WORK/remote-kind"
		kind_config | remote "cat > $REMOTE_DIR/kind.yaml"
		remote "$rkind create cluster --name $CLUSTER --config $REMOTE_DIR/kind.yaml --kubeconfig $REMOTE_DIR/kubeconfig --wait 120s"
		remote "cat $REMOTE_DIR/kubeconfig" >"$KCFG"
		chmod 600 "$KCFG"
		start_tunnel
	fi
	kk kubectl get nodes
}

cmd_seed() {
	start_tunnel
	# Server-side apply leaves no last-applied annotation in the YAML scene.
	kk kubectl apply --server-side -f "$REPO/docs/demo/seed.yaml"
	kk kubectl -n shop rollout status deploy/web --timeout=180s
	kk kubectl -n shop rollout status deploy/api --timeout=180s
	kk kubectl -n batch rollout status deploy/worker --timeout=180s
}

cmd_down() {
	if [ -z "$SSH_DEST" ]; then
		HOME=$WORK/home kind delete cluster --name "$CLUSTER" --kubeconfig "$KCFG" || true
	else
		local rkind=kind
		[ -f "$WORK/remote-kind" ] && rkind=$(cat "$WORK/remote-kind")
		remote "$rkind delete cluster --name $CLUSTER --kubeconfig $REMOTE_DIR/kubeconfig; rm -rf $REMOTE_DIR" || true
		stop_tunnel
	fi
	rm -rf "$WORK" "$LOCAL"
}

# --- driving kc ---------------------------------------------------------------

FRAMES=$WORK/frames
BH_LOG=$WORK/bh.log
BH_FIFO=$WORK/bh.fifo
frame_no=0
marker_no=0

bh() { printf '%s\n' "$*" >&3; }
key() { bh "key $1"; }
meta() { bh "text $(printf '\033')$1"; }
typetext() { bh "text $1"; }

# screen prints the current screen as plain text.
screen() {
	marker_no=$((marker_no + 1))
	local mark="demo-mark-$marker_no" start
	start=$(wc -c <"$BH_LOG")
	bh "screen plain"
	bh "$mark"
	# The driver answers an unknown command with its name, which marks the
	# end of the screen dump.
	local i=0 out
	while :; do
		out=$(tail -c +"$((start + 1))" "$BH_LOG")
		case $out in *"unknown command \"$mark\""*) break ;; esac
		i=$((i + 1))
		[ $i -lt 100 ] || die "the driver did not answer"
		sleep 0.05
	done
	printf '%s\n' "$out"
}

# wait_for waits until the screen shows the text (a fixed string).
wait_for() {
	local text=$1 timeout=${2:-20} start
	start=$(date +%s)
	while ! shows "$text"; do
		if [ $(($(date +%s) - start)) -ge "$timeout" ]; then
			screen >&2
			die "timed out after ${timeout}s waiting for \"$text\" on the screen"
		fi
		sleep 0.3
	done
}

# shows reports whether the screen shows the text now. The screen is read
# first so that grep -q cannot cut the pipe and fail it under pipefail.
shows() {
	local s
	s=$(screen)
	grep -qF -- "$1" <<<"$s"
}

# snap takes a frame and shows it for the given number of seconds.
snap() {
	local seconds=$1 name
	frame_no=$((frame_no + 1))
	name=$(printf 'f%04d.png' "$frame_no")
	bh "snapshot $FRAMES/$name color"
	printf "file '%s'\nduration %s\n" "$name" "$seconds" >>"$FRAMES/list.txt"
	local i=0
	while [ ! -s "$FRAMES/$name" ]; do
		i=$((i + 1))
		[ $i -lt 100 ] || die "snapshot $name was not written"
		sleep 0.05
	done
}

cmd_record() {
	start_tunnel
	rm -rf "$FRAMES"
	mkdir -p "$FRAMES" "$WORK/bin"
	(cd "$REPO" && go build -o "$WORK/bin/kc" ./cmd/kc && go build -o "$WORK/bin/bubbleheadless" ./cmd/bubbleheadless)
	# Undo what an earlier recording changed, so that record can run again.
	kk kubectl uncordon kc-demo-worker
	kk kubectl -n shop scale deploy/web --replicas=2
	kk kubectl -n shop rollout status deploy/web --timeout=180s
	local n=0
	until [ "$(kk kubectl -n shop get pods -l app=web --no-headers | wc -l | tr -d ' ')" = 2 ]; do
		n=$((n + 1))
		[ $n -lt 60 ] || die "the web pods of an earlier run did not go away"
		sleep 1
	done
	rm -rf "$LOCAL"
	mkdir -p "$LOCAL"
	rm -f "$BH_FIFO" "$BH_LOG"
	mkfifo "$BH_FIFO"
	# Assert the target once more, then start kc on the demo kubeconfig only.
	# The environment is empty apart from what kc needs, and PS1 keeps user
	# and host names out of the integrated terminal.
	kk true
	(cd "$LOCAL" && env -i PATH="$PATH" HOME="$WORK/home" KUBECONFIG="$KCFG" TZ=UTC LANG=en_US.UTF-8 \
		SHELL=/bin/sh PS1='$ ' PWD="$LOCAL" \
		"$WORK/bin/bubbleheadless" -cols "$COLS" -rows "$ROWS" -- "$WORK/bin/kc" --context "$CONTEXT" <"$BH_FIFO" >"$BH_LOG" 2>&1) &
	local bh_pid=$!
	exec 3>"$BH_FIFO"
	trap 'bh exit 2>/dev/null || true; exec 3>&-; kill '"$bh_pid"' 2>/dev/null || true; '"${ON_EXIT:-:}" EXIT

	scenes

	bh exit
	exec 3>&-
	wait "$bh_pid" || true
	trap "${ON_EXIT:-}" EXIT
	log "recorded $frame_no frames in $FRAMES"
}

cmd_gif() {
	[ -s "$FRAMES/list.txt" ] || die "no frames; run '$0 record' first"
	# The concat demuxer needs the last file once more to honor its duration.
	cp "$FRAMES/list.txt" "$FRAMES/concat.txt"
	grep '^file' "$FRAMES/list.txt" | tail -1 >>"$FRAMES/concat.txt"
	# One GIF frame per snapshot with its own delay; a shared palette without
	# dithering keeps the terminal colors flat and the file small.
	ffmpeg -loglevel error -y -f concat -safe 0 -i "$FRAMES/concat.txt" \
		-vf "split[a][b];[a]palettegen=max_colors=64:stats_mode=full[p];[b][p]paletteuse=dither=none:diff_mode=rectangle" \
		-fps_mode vfr -loop 0 "$OUT"
	log "wrote $OUT ($(wc -c <"$OUT" | tr -d ' ') bytes)"
}

# --- scenes -------------------------------------------------------------------
#
# Each step waits for the screen to show what it needs before it takes a
# frame, so a slow cluster changes the timing of the recording, not what it
# shows. The number after snap is how long the frame stays in the GIF.

# search moves the cursor to the first row that starts with the text (C-s).
search() {
	key ctrl+s
	typetext "$1"
	sleep 0.3
	key esc
	sleep 0.3
}

# enter searches a row, enters it and waits for the text to show.
enter() {
	key ctrl+s
	typetext "$1"
	sleep 0.3
	key enter
	wait_for "$2"
}

# shell_line runs a command on kc's shell line.
shell_line() {
	typetext "$1"
	sleep 0.3
	snap 0.8
	key enter
}

scene_panels() {
	# The root list grows while discovery runs; a search before it settles
	# can land on a row that moves away.
	wait_for "/storageclasses" 60
	sleep 2
	snap 1.2
	key ctrl+s
	typetext /nam
	sleep 0.3
	snap 0.7
	key enter
	wait_for "/kube-system"
	snap 0.9
	enter /sh "/namespaces/shop "
	enter /dep "/namespaces/shop/deployments "
	wait_for "/web"
	snap 1
	# The right panel lists the pods of the same namespace.
	key tab
	enter /nam "/kube-system"
	enter /sh "/namespaces/shop "
	enter /pod "/namespaces/shop/pods "
	wait_for "Running"
	snap 1.2
	key tab
}

scene_scale() {
	enter /web "/namespaces/shop/deployments/web "
	enter " Scale" "Scale Deployment"
	snap 0.8
	key backspace
	typetext 4
	sleep 0.3
	snap 0.6
	key enter
	wait_for "Replicas:    2 -> 4"
	snap 1.4
	key left
	key enter
	# The pod list on the right follows the scale-up live.
	local i
	for i in 1 2 3 4 5; do
		sleep 0.3
		snap 0.4
	done
	sleep 1
	snap 1
}

scene_yaml_and_logs() {
	key home
	key enter
	wait_for "/namespaces/shop/deployments "
	search /web
	key f3
	wait_for "kind: Deployment"
	snap 2
	key f3
	wait_for "/namespaces/shop/deployments "
	enter /api "/namespaces/shop/deployments/api "
	search " logs"
	key f3
	wait_for "payment declined" 30
	snap 1.5
	sleep 1
	snap 1.2
	key f3
	wait_for "/namespaces/shop/deployments/api "
}

scene_container_files() {
	key tab
	enter /web "/namespaces/shop/pods/web-"
	enter /cont "/containers "
	wait_for "/nginx"
	enter /nginx "/containers/nginx "
	enter /root "/nginx/root "
	wait_for "docker-entrypoint"
	snap 1
	enter etc "/root/etc "
	# Search only once the folder has loaded: Enter on an empty list would
	# take the cursor's /.. row and go back up.
	wait_for "│nginx "
	enter nginx "/root/etc/nginx "
	wait_for "nginx.conf"
	search nginx.c
	snap 1
	# A local directory in the left panel, then F5 copies the file there.
	key tab
	meta l
	wait_for "$(basename "$LOCAL")"
	snap 0.8
	key tab
	key f5
	wait_for "Copy from the container?"
	snap 1.6
	key enter
	wait_for "Copy finished"
	snap 1.6
	key enter
	wait_for " nginx.conf"
	snap 1
}

scene_drain() {
	# The right panel goes back to the pods, the left one to the nodes.
	shell_line "cd -n shop pods"
	wait_for "kind-kc-demo : shop"
	key tab
	meta l
	sleep 0.5
	shell_line "cd /nodes"
	wait_for "kc-demo-worker"
	search /kc-demo-w
	meta n
	wait_for "Node actions"
	snap 1
	key down
	key down
	key enter
	wait_for "Ignore DaemonSet pods"
	snap 1.2
	key enter
	wait_for "Drain node?"
	snap 2
	key left
	key enter
	# One frame per change of the progress row, so a slow eviction does not
	# stretch the GIF.
	local i=0 row last=""
	while ! shows "Drained node"; do
		i=$((i + 1))
		[ $i -lt 300 ] || die "the drain did not finish"
		row=$(screen | grep -E 'kc-demo-worker +[a-z]+ +[0-9]' || true)
		if [ -n "$row" ] && [ "$row" != "$last" ]; then
			snap 0.6
			last=$row
		fi
		sleep 0.2
	done
	snap 1.5
	key enter
	wait_for "SchedulingDisabled"
	snap 1.2
	meta n
	wait_for "Node actions"
	key down
	key enter
	wait_for "Uncordon node?"
	snap 1
	key left
	key enter
	sleep 2
	snap 1.2
}

scene_terminal() {
	key ctrl+o
	wait_for "Return to panels"
	typetext "kubectl -n shop get deploy,pods"
	sleep 0.3
	snap 0.8
	key enter
	wait_for "deployment.apps/web" 20
	sleep 1
	snap 2.5
	key ctrl+o
	sleep 1
	snap 1.2
}

scenes() {
	scene_panels
	scene_scale
	scene_yaml_and_logs
	scene_container_files
	scene_drain
	scene_terminal
}

case "${1:-all}" in
up) cmd_up ;;
seed) cmd_seed ;;
record) cmd_record ;;
gif) cmd_gif ;;
down) cmd_down ;;
all)
	ON_EXIT=cmd_down
	trap cmd_down EXIT
	cmd_up
	cmd_seed
	cmd_record
	cmd_gif
	;;
*) die "unknown command $1" ;;
esac
