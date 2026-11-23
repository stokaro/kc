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
#   KC_DEMO_SCENES       scenes to record, in order (default: all, see SCENES);
#                        each scene starts where the one before it ends
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
METRICS_SERVER=https://github.com/kubernetes-sigs/metrics-server/releases/download/v0.8.0/components.yaml
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
	# metrics-server feeds the Info panel and the overview's capacity rows.
	# kind's kubelets serve self-signed certificates. It runs on the control
	# plane, so the drain scene neither stops it nor has to delete its
	# emptyDir. --force-conflicts takes the args back from an earlier patch,
	# which the guard below then adds again.
	kk kubectl apply --server-side --force-conflicts -f "$METRICS_SERVER"
	local args
	args=$(kk kubectl -n kube-system get deploy metrics-server -o 'jsonpath={.spec.template.spec.containers[0].args}')
	case $args in
	*--kubelet-insecure-tls*) ;;
	*) kk kubectl -n kube-system patch deploy metrics-server --type=json \
		-p '[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}]' ;;
	esac
	kk kubectl -n kube-system patch deploy metrics-server --type=strategic -p \
		'{"spec":{"template":{"spec":{"nodeSelector":{"node-role.kubernetes.io/control-plane":""},"tolerations":[{"key":"node-role.kubernetes.io/control-plane","operator":"Exists","effect":"NoSchedule"}]}}}}'
	kk kubectl -n shop rollout status deploy/web --timeout=180s
	kk kubectl -n shop rollout status deploy/api --timeout=180s
	kk kubectl -n batch rollout status deploy/worker --timeout=180s
	kk kubectl -n kube-system rollout status deploy/metrics-server --timeout=180s
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

# hotlist prints the saved views of the hotlist scene, in the format kc
# writes to ~/.kc/hotlist.yaml.
hotlist() {
	cat <<EOF
entries:
- context: $CONTEXT
  namespace: shop
  steps: [shop//v1/pods]
  path: /pods
  name: not running
  view:
    columns: normal
    order: name
    filter: status!=Running
- context: $CONTEXT
  namespace: batch
  steps: [batch//v1/pods]
  path: /pods
  name: restarting
  view:
    columns: normal
    order: -restarts
    filter: restarts>0
- context: $CONTEXT
  namespace: kube-system
  steps: [kube-system/apps/v1/deployments]
  path: /deployments
EOF
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
	# kc starts without saved settings, history or bookmarks, apart from the
	# saved views the hotlist scene opens.
	rm -rf "$WORK/home/.kc"
	mkdir -p "$WORK/home/.kc"
	hotlist >"$WORK/home/.kc/hotlist.yaml"
	rm -f "$BH_FIFO" "$BH_LOG"
	mkfifo "$BH_FIFO"
	# Assert the target once more, then start kc on the demo kubeconfig only.
	# The environment is empty apart from what kc needs, and PS1 keeps user
	# and host names out of the integrated terminal.
	kk true
	(cd "$LOCAL" && env -i PATH="$PATH" HOME="$WORK/home" KUBECONFIG="$KCFG" TZ=UTC LANG=en_US.UTF-8 \
		SHELL=/bin/sh PS1='$ ' PWD="$LOCAL" \
		"$WORK/bin/bubbleheadless" -cols "$COLS" -rows "$ROWS" -- "$WORK/bin/kc" --context "$CONTEXT" -n shop <"$BH_FIFO" >"$BH_LOG" 2>&1) &
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
# shows. The number after snap is how long the frame stays in the GIF; the
# names below keep the pacing the same in every scene.

FIRST=3   # the first frame of a scene, with its new caption
READ=3    # a frame with text to read
STEP=1.8  # an ordinary step
TYPE=1    # a frame that shows typed text
ANIM=0.8  # one step of a live change (scale-up, drain progress)

# caption sets the caption the driver draws over the following frames.
caption() { bh "caption $*"; }

# search moves the cursor to the first row that starts with the text (C-s).
search() {
	key ctrl+s
	typetext "$1"
	sleep 0.3
	# A lone Esc counts once the Esc timeout (1s) passes: Esc+key is Meta.
	key esc
	sleep 1.2
}

# enter searches a row, enters it and waits for the text to show.
enter() {
	key ctrl+s
	typetext "$1"
	sleep 0.3
	key enter
	wait_for "$2"
}

# shell_line types a command on kc's shell line, shows it for the given
# number of seconds and runs it.
shell_line() {
	typetext "$1"
	sleep 0.3
	snap "${2:-$TYPE}"
	key enter
}

# widen makes the left panel 75% wide (50, 66, 75); narrow gives the right
# panel its half back (25, 33, 50). M-w only ever widens the active panel.
widen() {
	meta w
	meta w
	sleep 0.5
}

narrow() {
	key tab
	meta w
	meta w
	key tab
	sleep 0.5
}

scene_intro() {
	caption "kc: a two-panel Kubernetes TUI in the style of Midnight Commander"
	# The root list grows while discovery runs; a search before it settles
	# can land on a row that moves away. Info fills once metrics arrive.
	wait_for "/servicecidrs" 60
	wait_for "Top pods by memory in namespace shop" 60
	wait_for "shop/web-" 90
	sleep 2
	snap 3.5
}

scene_search() {
	caption "Quick search (C-s): type the start of a name, Enter opens the row"
	key ctrl+s
	typetext /dep
	sleep 0.4
	snap $FIRST
	key enter
	wait_for "/checkout"
	sleep 0.5
	snap $STEP
	search /web
	wait_for "Top pods of deployments.apps shop/web"
	sleep 1
	caption "The Info panel follows the cursor: usage, pods, top pods"
	snap $FIRST
}

scene_overview() {
	caption "Cluster overview (M-i): what needs attention in this namespace"
	meta i
	wait_for "/attention"
	wait_for "/pods image pull err"
	sleep 1.5
	snap $FIRST
	enter /att "/overview/attention "
	search "/pods image"
	wait_for "Scope: namespace shop"
	sleep 0.5
	caption "Info explains the finding under the cursor"
	snap $FIRST
	key enter
	wait_for "/overview/attention/pods image pull errors"
	wait_for "ImagePullBackOff"
	sleep 0.5
	caption "Enter lists the objects behind a finding"
	snap $READ
}

scene_diagnosis() {
	caption "/diagnosis explains why a Deployment is broken"
	shell_line "cd deploy/checkout" $FIRST
	wait_for "/deployments/checkout "
	wait_for "/diagnosis"
	enter /diag "/deployments/checkout/diagnosis "
	wait_for " error"
	widen
	search " error"
	wait_for "The rollout does not progress"
	snap $READ
	# The second finding: the pod's image cannot be pulled.
	key ctrl+s
	typetext " error"
	key ctrl+s
	sleep 0.3
	key esc
	sleep 1.2
	wait_for "cannot pull its image"
	snap $READ
	caption "... and why a Service has no endpoints"
	shell_line "cd svc/payments" $FIRST
	wait_for "/services/payments "
	enter /diag "/services/payments/diagnosis "
	search " error"
	wait_for "No running pod match"
	snap $READ
	narrow
}

scene_menu() {
	caption "F9 opens the menu bar, as in mc; each entry shows its key"
	# F9 starts on the Left menu; File is the next one.
	key f9
	key right
	key down
	wait_for "Quick search"
	sleep 0.3
	snap $FIRST
	key right
	key right
	key right
	wait_for "│*Info"
	sleep 0.3
	caption "The Right menu switches the right panel from Info to a list"
	snap $READ
	key enter
	wait_for "Cluster overview: capacity, health, risks, activity"
	# The right panel lists the pods of the namespace.
	key tab
	enter /pods "/pods "
	wait_for "Running"
	key tab
	sleep 0.5
	snap $STEP
}

scene_scale() {
	caption "/timeline records every change kc observes on an object"
	shell_line "cd deploy/web" $FIRST
	wait_for "/deployments/web "
	enter /tim "/deployments/web/timeline "
	wait_for "│initial "
	sleep 0.5
	snap $STEP
	key home
	key enter
	wait_for " Scale"
	caption "Scale... asks first; the pods on the right follow live"
	enter " Scale" "Scale Deployment"
	snap $FIRST
	key backspace
	typetext 4
	sleep 0.3
	snap $TYPE
	key enter
	wait_for "Replicas:    2 -> 4"
	snap $READ
	key left
	key enter
	local i
	for i in 1 2 3 4; do
		sleep 0.4
		snap $ANIM
	done
	sleep 1
	snap $STEP
	enter /tim "/deployments/web/timeline "
	wait_for "│spec "
	widen
	key end
	# The rows end with the revisions, the note and "stop recording"; the
	# spec change is the oldest entry after the initial one. The line under
	# the list shows the whole change of the row under the cursor.
	local n=0
	until shows "; spec.replicas"; do
		n=$((n + 1))
		[ $n -lt 12 ] || die "no timeline row with the spec change"
		key up
		sleep 0.3
	done
	caption "The timeline shows the scale-up, newest first"
	snap $FIRST
	key f3
	wait_for "+  replicas: 4"
	sleep 0.3
	caption "F3 on an entry diffs it against the revision before"
	snap $FIRST
	key f3
	wait_for "/deployments/web/timeline "
	narrow
}

scene_yaml() {
	caption "F3 views any object; this is the YAML of the Deployment"
	key home
	key enter
	wait_for "/deployments/web "
	key home
	key enter
	wait_for "/deployments "
	search /web
	key f3
	wait_for "kind: Deployment"
	sleep 0.5
	snap $FIRST
	key f3
	wait_for "/deployments "
}

scene_compare() {
	caption "C-x C-d compares the objects under the two cursors"
	# The right panel lists the Deployments too, with api under its cursor;
	# web is under the left one.
	key tab
	typetext "cd deployments"
	key enter
	# Both panels list the same rows now, so there is no text to wait for.
	sleep 2
	search /api
	key tab
	key ctrl+x
	key ctrl+d
	# "Reading both manifests..." took 30 to 40 seconds in the recordings.
	wait_for "+++ right/" 90
	sleep 0.5
	snap $FIRST
	key f3
	wait_for "/deployments "
	# Back to the pods on the right for the later scenes.
	key tab
	typetext "cd pods"
	key enter
	wait_for "/web-"
	key tab
	sleep 0.5
}

scene_logs() {
	caption "Merged logs of every pod of a Deployment, JSON lines formatted"
	enter /api "/deployments/api "
	search " logs"
	key f3
	wait_for "payment declined" 30
	sleep 1
	snap $FIRST
	caption "M-g filters by JSON fields: level=error,warn"
	meta g
	wait_for "Log filter"
	typetext "level=error,warn"
	sleep 0.4
	snap $READ
	key enter
	sleep 1.5
	snap $READ
	key f3
	wait_for "/deployments/api "
}

scene_files() {
	caption "Container files: browse /root of a running container"
	key tab
	wait_for "web-"
	enter /web "/pods/web-"
	enter /cont "/containers "
	wait_for "/nginx"
	enter /nginx "/containers/nginx "
	enter /root "/nginx/root "
	wait_for "docker-entrypoint"
	sleep 0.3
	snap $FIRST
	enter etc "/root/etc "
	# Search only once the folder has loaded: Enter on an empty list would
	# take the cursor's /.. row and go back up.
	wait_for "│nginx "
	enter nginx "/root/etc/nginx "
	wait_for "nginx.conf"
	search nginx.c
	snap $STEP
	# A local directory in the left panel, then F5 copies the file there.
	caption "M-l opens a local directory; F5 copies out of the container"
	key tab
	meta l
	wait_for "$(basename "$LOCAL")"
	key tab
	sleep 0.3
	snap $FIRST
	key f5
	wait_for "Copy from the container?"
	snap $READ
	key enter
	wait_for "Copy finished"
	snap $READ
	key enter
	wait_for " nginx.conf"
	snap $STEP
}

# node_actions opens the node menu (M-n) on the node under the cursor. For
# up to a minute after a log stream or a container file session ended, M-n
# answered "Select a node first" in the recordings, so it tries again; the
# GIF shows only the menu.
node_actions() {
	local i
	for i in $(seq 30); do
		meta n
		sleep 3
		if shows "Node actions"; then
			return 0
		fi
	done
	die "M-n did not open the node actions"
}

scene_drain() {
	caption "Node actions (M-n): drain a node with progress, then uncordon"
	# The left panel goes to the nodes; the right one keeps the pods, which
	# go Pending while the only worker is drained.
	shell_line "cd /nodes" $FIRST
	wait_for "kc-demo-worker"
	# The arrow keys, not a quick search: after C-s the first M-n presses
	# answered "Select a node first" in the recordings. The worker sorts
	# after the control plane.
	sleep 2
	key down
	key down
	wait_for "│kc-demo-worker (v1)"
	node_actions
	snap $STEP
	key down
	key down
	key enter
	wait_for "Ignore DaemonSet pods"
	snap $READ
	key enter
	wait_for "Drain node?"
	snap $READ
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
			snap $ANIM
			last=$row
		fi
		sleep 0.2
	done
	snap $READ
	key enter
	wait_for "SchedulingDisabled"
	snap $STEP
	caption "Uncordon makes the node schedulable again; the pods come back"
	node_actions
	key down
	key enter
	wait_for "Uncordon node?"
	snap $FIRST
	key left
	key enter
	sleep 2
	snap $STEP
	# The files scene needs a running web pod again.
	kk kubectl -n shop rollout status deploy/web --timeout=180s >/dev/null
}

scene_hotlist() {
	caption "Hotlist (C-\\): saved places and views with their filter and sort"
	key 0x1c
	wait_for "not running"
	sleep 0.3
	snap $FIRST
	key enter
	wait_for "kc also filters: status!=Running"
	wait_for "/checkout-"
	sleep 0.5
	snap $READ
	key 0x1c
	wait_for "restarting"
	key down
	key enter
	wait_for "kc also filters: restarts>0"
	wait_for "/report-"
	sleep 1
	caption "Another saved view: pods in batch that restart, most restarts first"
	snap $READ
}

scene_terminal() {
	caption "C-o: the integrated terminal follows the panel's context and namespace"
	key ctrl+o
	wait_for "Return to panels"
	typetext "kubectl get deploy,pods"
	sleep 0.3
	snap $FIRST
	key enter
	wait_for "deployment.apps/report" 20
	sleep 1
	snap $READ
	key ctrl+o
	sleep 1
	caption "C-o again returns to the panels"
	snap $FIRST
}

# The scenes in recording order. KC_DEMO_SCENES picks some of them, such as
# the first few, to check a change quickly; each scene starts where the one
# before it ends.
SCENES="intro search overview diagnosis menu drain scale yaml compare logs files hotlist terminal"

scenes() {
	local s
	for s in ${KC_DEMO_SCENES:-$SCENES}; do
		log "scene $s"
		"scene_$s"
	done
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
