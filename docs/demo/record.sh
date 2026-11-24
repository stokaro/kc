#!/usr/bin/env bash
# Records docs/demo.gif: creates a throwaway kind cluster, seeds demo objects,
# drives kc through cmd/bubbleheadless, renders the frames to a GIF and
# deletes the cluster again. See docs/demo/README.md. The cluster and driver
# functions are in docs/demo/lib.sh, which screenshots/capture.sh shares.
#
# Usage: docs/demo/record.sh [all|up|seed|record|gif|down]
#
# Environment:
#   KC_DEMO_SSH          optional ssh destination of a Docker host (Linux or macOS);
#                        kind then runs there and the API server is reached through
#                        an ssh tunnel
#   KC_DEMO_REMOTE_KIND  local path of a kind binary for that host's OS and CPU, to
#                        upload when the remote host has no kind on PATH
#   KC_DEMO_PORT         API server port on 127.0.0.1 (default 39158)
#   KC_DEMO_WORK         work directory (default ${TMPDIR:-/tmp}/kc-demo)
#   KC_DEMO_LOCAL        local directory kc starts in; the copy scene writes there
#                        and the GIF shows its path (default /tmp/kc-demo-downloads)
#   KC_DEMO_OUT          output GIF (default docs/demo.gif)
#   KC_DEMO_DASH_WAIT    seconds the dashboard collects samples before its
#                        first frame (default 240)
#   KC_DEMO_SCENES       scenes to record, in order (default: all, see SCENES);
#                        each scene starts where the one before it ends
set -euo pipefail

CLUSTER=kc-demo
PORT=${KC_DEMO_PORT:-39158}
REPO=$(cd "$(dirname "$0")/../.." && pwd)
WORK=${KC_DEMO_WORK:-${TMPDIR:-/tmp}/kc-demo}
OUT=${KC_DEMO_OUT:-$REPO/docs/demo.gif}
LOCAL=${KC_DEMO_LOCAL:-/tmp/kc-demo-downloads}
SSH_DEST=${KC_DEMO_SSH:-}
REMOTE_KIND=${KC_DEMO_REMOTE_KIND:-}
DASH_WAIT=${KC_DEMO_DASH_WAIT:-240}
COLS=120
ROWS=34
LIB_TAG=demo
# shellcheck source=docs/demo/lib.sh
. "$REPO/docs/demo/lib.sh"

cmd_up() { cluster_up; }

cmd_seed() {
	start_tunnel
	# Server-side apply leaves no last-applied annotation in the manifests
	# kc shows.
	kk kubectl apply --server-side -f "$REPO/docs/demo/seed.yaml"
	seed_metrics_server
	local d
	for d in shop/web shop/api shop/loadgen batch/worker; do
		kk kubectl -n "${d%/*}" rollout status "deploy/${d#*/}" --timeout=180s
	done
	# Info and the dashboard need metrics; the first ones take about a
	# minute after metrics-server starts.
	local n=0
	until kk kubectl -n shop top pods >/dev/null 2>&1; do
		n=$((n + 1))
		[ $n -lt 60 ] || die "metrics-server serves no pod metrics"
		sleep 5
	done
}

cmd_down() {
	cluster_down
	rm -rf "$WORK" "$LOCAL"
}

FRAMES=$WORK/frames
frame_no=0

# snap takes a frame and shows it for the given number of seconds. It
# waits until the Kitty images on the screen stop changing, so an Info bar
# is not caught as text while its image is on its way.
snap() {
	local seconds=$1 name
	frame_no=$((frame_no + 1))
	name=$(printf 'f%04d.png' "$frame_no")
	settle_images
	write_png "$FRAMES/$name"
	printf "file '%s'\nduration %s\n" "$name" "$seconds" >>"$FRAMES/list.txt"
}

# kc_config prints the settings kc records with. The dashboard draws its
# charts, and Info its bars and history, as Kitty graphics images, as in
# iTerm2 3.7, kitty and Ghostty; the driver's color snapshots show them.
# With auto kc would draw text, since the driver hides the outer terminal
# from it.
kc_config() {
	cat <<'EOF'
charts:
  graphics: kitty
EOF
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
	mkdir -p "$FRAMES"
	build_kc
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
	# kc starts without saved settings, history or bookmarks, apart from
	# kc_config and the saved views the hotlist scene opens.
	rm -rf "$WORK/home/.kc"
	mkdir -p "$WORK/home/.kc"
	kc_config >"$WORK/home/.kc/config.yaml"
	hotlist >"$WORK/home/.kc/hotlist.yaml"
	start_kc --context "$CONTEXT" -n shop

	scenes

	stop_kc
	log "recorded $frame_no frames in $FRAMES"
}

cmd_gif() {
	[ -s "$FRAMES/list.txt" ] || die "no frames; run '$0 record' first"
	# The concat demuxer needs the last file once more to honor its duration.
	cp "$FRAMES/list.txt" "$FRAMES/concat.txt"
	grep '^file' "$FRAMES/list.txt" | tail -1 >>"$FRAMES/concat.txt"
	# One GIF frame per snapshot with its own delay; a shared palette without
	# dithering keeps the terminal colors flat and the file small. With 64
	# colors the greens of the chart and gauge images, a small share of the
	# pixels, came out olive; 128 keep them.
	ffmpeg -loglevel error -y -f concat -safe 0 -i "$FRAMES/concat.txt" \
		-vf "split[a][b];[a]palettegen=max_colors=128:stats_mode=full[p];[b][p]paletteuse=dither=none:diff_mode=rectangle" \
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

# shell_line types a command on kc's shell line, shows it for the given
# number of seconds and runs it.
shell_line() {
	typetext "$1"
	sleep 0.3
	snap "${2:-$TYPE}"
	key enter
}

scene_intro() {
	caption "kc: a two-panel Kubernetes TUI in the style of Midnight Commander"
	# The root list grows while discovery runs; a search before it settles
	# can land on a row that moves away. Info fills once metrics arrive.
	wait_for "/other types" 60
	wait_for "Top pods by memory in namespace shop" 60
	wait_for "shop/web-" 90
	sleep 2
	snap 3.5
}

# select_workload moves the selection of the dashboard's list to the
# Deployment named. The list sorts by usage, so the row is found by what
# the screen says, not by its position.
select_workload() {
	local i
	for i in 1 2 3 4 5 6; do
		key up
	done
	for i in 1 2 3 4 5 6; do
		sleep 0.5
		shows "Pods of Deployment $1 (Enter to open)" && return 0
		key down
	done
	die "the dashboard list did not select Deployment $1"
}

scene_dashboard() {
	caption "C-x g: a full-screen dashboard of CPU, memory and Events"
	key ctrl+x
	key g
	wait_for "namespace shop  LIVE" 30
	# + zooms the session history to its shortest range, 5 minutes.
	key +
	wait_for "last 5 minutes"
	# kc draws the Metrics API samples it reads while a dashboard is
	# open, one every 15 seconds. Halfway, a restart of loadgen puts a
	# rollout on the Events lane and new pods on the charts.
	log "collecting dashboard samples for ${DASH_WAIT}s"
	sleep $((DASH_WAIT / 2))
	kk kubectl -n shop rollout restart deploy/loadgen >/dev/null
	sleep $((DASH_WAIT - DASH_WAIT / 2))
	wait_for_images 2
	snap 4
	# A drilldown starts with the history of its row, which the
	# namespace read along.
	select_workload loadgen
	key enter
	wait_for "› Deployment loadgen  LIVE"
	wait_for_images 2
	caption "Enter opens a workload, with the history read so far"
	snap $FIRST
	# A click on the state at the right end of the top row opens the
	# Range menu under it.
	bh "mouse 0 $((COLS - 6)) 0 press"
	bh "mouse 0 $((COLS - 6)) 0 release"
	wait_for "Last 15 minutes"
	caption "A click on the state at the top right picks the range"
	snap $READ
	# Esc closes the menu, F10 the dashboard; the panels are as they were.
	key esc
	sleep 1.2
	key f10
	wait_for "/other types"
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
	shell_line "cd deploy/checkout"
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
	shell_line "cd svc/payments"
	wait_for "/services/payments "
	enter /diag "/services/payments/diagnosis "
	search " error"
	wait_for "No running pod match"
	snap $READ
	narrow
}

scene_menu() {
	caption "F9 opens the menu bar, as in mc; each entry shows its key"
	# F9 highlights the title of the menu that was open last, which
	# depends on the scenes before; the highlighted letter of a title
	# drops that menu down.
	key f9
	key f
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
	shell_line "cd deploy/web"
	wait_for "/deployments/web "
	enter /tim "/deployments/web/timeline "
	wait_for "│initial "
	sleep 0.5
	snap $READ
	key home
	key enter
	wait_for " Scale"
	caption "Scale... asks first; the pods on the right follow live"
	enter " Scale" "Scale Deployment"
	key backspace
	typetext 4
	sleep 0.3
	snap $FIRST
	key enter
	wait_for "Replicas:    2 -> 4"
	snap $READ
	key left
	key enter
	local i
	for i in 1 2 3; do
		sleep 0.5
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
	# the list shows the revision, uid and change of the row under the
	# cursor. The row itself shows the change too, so match the line with
	# the uid; the generation numbers differ between clusters.
	local n=0
	until screen | grep -q 'uid .*; spec\.replicas'; do
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
	# Up to the Deployments, where the logs scene starts.
	key home
	key enter
	wait_for "/deployments/web "
	key home
	key enter
	wait_for "/deployments "
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
	key enter
	wait_for " nginx.conf"
	snap $STEP
}

scene_drain() {
	caption "Node actions (M-n): drain a node with progress, then uncordon"
	# The left panel goes to the nodes; the right one keeps the pods, which
	# go Pending while the only worker is drained.
	shell_line "cd /nodes"
	# Wait for a header only the node table has: the worker's name is on
	# the screen before the nodes load, in the pods' Node column.
	wait_for "Roles"
	# The arrow keys, not a quick search: after C-s the first M-n presses
	# answered "Select a node first" in the recordings. The worker sorts
	# after the control plane.
	sleep 2
	key down
	key down
	wait_for "│kc-demo-worker (v1)"
	node_actions
	snap $FIRST
	key down
	key down
	key enter
	wait_for "Ignore DaemonSet pods"
	# loadgen keeps data in an emptyDir, which a drain refuses to delete
	# unless asked. The focus starts on Drain; two fields up is the box.
	key up
	key up
	key space
	key down
	key down
	wait_for "[x] Delete emptyDir data"
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
	snap $STEP
	key enter
	wait_for "SchedulingDisabled"
	# The evicted pods come back Pending: the control plane is tainted.
	wait_for "Pending"
	snap $STEP
	node_actions
	key down
	key enter
	wait_for "Uncordon node?"
	key left
	key enter
	sleep 2
	caption "Uncordon makes the node schedulable again; the pods come back"
	snap $FIRST
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
SCENES="intro dashboard search overview diagnosis menu drain scale logs files hotlist terminal"

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
