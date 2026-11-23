#!/usr/bin/env bash
# Captures the screenshots in this folder: creates a throwaway kind cluster,
# seeds it with docs/demo/seed.yaml and screenshots/seed.yaml, drives kc
# through cmd/bubbleheadless at a fixed size and writes one color PNG per
# scene. See screenshots/README.md. The cluster and driver functions are in
# docs/demo/lib.sh, which docs/demo/record.sh shares.
#
# Usage: screenshots/capture.sh [all|up|seed|capture [scene|group...]|list|down]
#
#   up       create the kind cluster
#   seed     apply the demo objects, metrics-server and a Helm release
#   capture  write the PNGs of the given scenes or groups (default: all)
#   list     print the scenes in capture order
#   down     delete the cluster and the work directory
#   all      up, seed, capture and down; down runs also when a step fails
#
# Environment:
#   KC_SHOTS_CLUSTER      kind cluster name (default kc-shots)
#   KC_SHOTS_PORT         API server port on 127.0.0.1 (default 39159)
#   KC_SHOTS_SSH          optional ssh destination of a Linux Docker host; kind
#                         then runs there and the API server is reached
#                         through an ssh tunnel
#   KC_SHOTS_REMOTE_KIND  local path of a Linux kind binary to upload when the
#                         remote host has no kind on PATH
#   KC_SHOTS_WORK         work directory (default ${TMPDIR:-/tmp}/kc-shots)
#   KC_SHOTS_LOCAL        local directory kc starts in; the local file scenes
#                         show it (default /tmp/kc-shots)
#   KC_SHOTS_DASH_WAIT    seconds the dashboard collects samples before its
#                         first screenshot (default 300)
#   KC_SHOTS_COLS         terminal width kc runs in (default 150); the
#                         committed screenshots use the default
set -euo pipefail

CLUSTER=${KC_SHOTS_CLUSTER:-kc-shots}
PORT=${KC_SHOTS_PORT:-39159}
REPO=$(cd "$(dirname "$0")/.." && pwd)
WORK=${KC_SHOTS_WORK:-${TMPDIR:-/tmp}/kc-shots}
LOCAL=${KC_SHOTS_LOCAL:-/tmp/kc-shots}
SSH_DEST=${KC_SHOTS_SSH:-}
REMOTE_KIND=${KC_SHOTS_REMOTE_KIND:-}
# shellcheck disable=SC2034 # used by scenes/04-dashboard.sh
DASH_WAIT=${KC_SHOTS_DASH_WAIT:-300}
COLS=${KC_SHOTS_COLS:-150}
ROWS=42
WORKERS=2
LIB_TAG=shots
SHOTS=$REPO/screenshots
# shellcheck source=docs/demo/lib.sh
. "$REPO/docs/demo/lib.sh"

# The second context of the kubeconfig kc starts with. It points at the same
# cluster, so the context picker and the compare with another context (C-x c)
# have something to show.
MIRROR=$CLUSTER-mirror
KC_KUBECONFIG=$KCFG:$WORK/mirror.yaml

cmd_up() { cluster_up; }

cmd_seed() {
	start_tunnel
	# Server-side apply leaves no last-applied annotation in the YAML scenes.
	# --force-conflicts takes catalog back from the revisions below, so seed
	# can run again.
	kk kubectl apply --server-side -f "$REPO/docs/demo/seed.yaml"
	kk kubectl apply --server-side --force-conflicts -f "$SHOTS/seed.yaml"
	seed_metrics_server
	local d
	for d in shop/web shop/api shop/catalog shop/loadgen batch/worker; do
		kk kubectl -n "${d%/*}" rollout status "deploy/${d#*/}" --timeout=180s
	done
	# Two more revisions of catalog for /rollout: a new image, then a new
	# env value.
	kk kubectl -n shop set image deploy/catalog catalog=busybox:1.37
	kk kubectl -n shop rollout status deploy/catalog --timeout=180s
	kk kubectl -n shop set env deploy/catalog MODE=v2
	kk kubectl -n shop rollout status deploy/catalog --timeout=180s
	# A Helm release with two revisions for /helm.
	kk helm upgrade --install banner "$SHOTS/chart" -n shop --wait
	kk helm upgrade banner "$SHOTS/chart" -n shop --set message="Winter sale" --wait
	# The Info panel and the dashboard need metrics; the first ones take
	# about a minute after metrics-server starts.
	local n=0
	until kk kubectl -n shop top pods >/dev/null 2>&1; do
		n=$((n + 1))
		[ $n -lt 60 ] || die "metrics-server serves no pod metrics"
		sleep 5
	done
}

cmd_down() {
	stop_kc_if_running
	cluster_down
	rm -rf "$WORK" "$LOCAL"
}

# --- preparing a kc session ---------------------------------------------------

# mirror_kubeconfig writes the second context. It names the cluster and user
# of the kind kubeconfig, so it holds no credentials of its own.
mirror_kubeconfig() {
	cat >"$WORK/mirror.yaml" <<EOF
apiVersion: v1
kind: Config
contexts:
- name: $MIRROR
  context:
    cluster: $CONTEXT
    user: $CONTEXT
    namespace: shop
EOF
}

# local_files fills the directory kc starts in: a manifest to compare with
# the live Deployment, a short text file, and a log large enough for the
# paged viewer (over 2 MiB).
local_files() {
	rm -rf "$LOCAL"
	mkdir -p "$LOCAL/manifests"
	cat >"$LOCAL/manifests/web.yaml" <<'EOF'
# Desired state of shop/web, as a kustomize build would render it.
apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  namespace: shop
  labels:
    app: web
spec:
  replicas: 3
  selector:
    matchLabels:
      app: web
  template:
    metadata:
      labels:
        app: web
    spec:
      terminationGracePeriodSeconds: 2
      containers:
        - name: nginx
          image: nginx:1.28-alpine
          ports:
            - containerPort: 80
              name: http
          resources:
            requests:
              cpu: 10m
              memory: 16Mi
EOF
	cat >"$LOCAL/notes.md" <<'EOF'
# Shop on-call notes

- checkout pulls an image tag that does not exist.
- payments has no pods behind it.
- report in batch crash loops on a missing config file.
EOF
	local i=0
	{
		while [ $i -lt 40000 ]; do
			printf '2026-11-23T10:%02d:%02dZ GET /products/%d 200 %dms upstream=api-%d\n' \
				$((i / 60 % 60)) $((i % 60)) $((i % 500)) $((i * 7 % 90 + 3)) $((i % 3))
			i=$((i + 1))
		done
	} >"$LOCAL/access.log"
}

# hotlist prints the saved places and views of the hotlist scene, in the
# format kc writes to ~/.kc/hotlist.yaml.
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

kc_running=

stop_kc_if_running() {
	if [ -n "$kc_running" ]; then
		stop_kc
		kc_running=
	fi
}

# fresh starts a new kc session for a scene: without saved settings or
# history, apart from the hotlist entries, with the arguments given (default:
# the shop namespace). It waits until API discovery has listed the types.
fresh() {
	stop_kc_if_running
	rm -rf "$WORK/home/.kc"
	mkdir -p "$WORK/home/.kc"
	hotlist >"$WORK/home/.kc/hotlist.yaml"
	marker_no=0
	if [ $# -eq 0 ]; then
		set -- --context "$CONTEXT" -n shop
	fi
	start_kc "$@"
	kc_running=1
	menu_at=0
	# The root of all namespaces is longer than the panel, so its last
	# type is not on the screen.
	case " $* " in
	*" -A "*) wait_for "/services " 60 ;;
	*) wait_for "/servicecidrs" 60 ;;
	esac
	# The Info panel on the right says "loading" until the nodes, pods
	# and metrics have arrived.
	wait_for "pods measured" 60
	sleep 1
}

# --- scenes -------------------------------------------------------------------
#
# A scene writes one PNG, screenshots/<group>/<nn>-<name>.png. The scenes of
# a group are the functions scene_<group>_<name> in scenes/<nn>-<group>.sh,
# and <nn> is the place of the function in its file, so adding or removing a
# scene renumbers the ones after it. A scene id is <group>/<nn>-<name>; the
# number may be left out. A scene starts with fresh, or with after
# <group>/<name> when it goes on from where another scene ends; after runs
# that scene first unless it was the last one. So any scene can be captured
# alone.

current=
last_done=

# shot writes the PNG of the current scene, after the given pause (default
# half a second) for the screen to settle. A palette of 128 colors without
# dithering keeps the terminal colors exact and the file about a quarter of
# the driver's RGB PNG.
shot() {
	local file
	file=$(scene_file "$current")
	mkdir -p "$(dirname "$file")"
	sleep "${1:-0.5}"
	write_png "$WORK/shot.png"
	ffmpeg -loglevel error -y -i "$WORK/shot.png" \
		-vf "split[a][b];[a]palettegen=max_colors=128:reserve_transparent=0[p];[b][p]paletteuse=dither=none" \
		"$file"
	log "wrote ${file#"$REPO"/}"
}

# scene_key drops the number from a scene id: dashboard/01-live and
# dashboard/live both give dashboard/live.
scene_key() {
	local group=${1%%/*} name=${1#*/}
	printf '%s/%s\n' "$group" "${name#[0-9][0-9]-}"
}

# scene_file prints the PNG path of a scene, with its number.
scene_file() {
	local key id
	key=$(scene_key "$1")
	id=$(scene_ids | grep -E "^${key%%/*}/[0-9][0-9]-${key#*/}\$" || true)
	[ -n "$id" ] || die "no scene $1"
	printf '%s/%s.png\n' "$SHOTS" "$id"
}

after() {
	local key
	key=$(scene_key "$1")
	[ "$last_done" = "$key" ] && return 0
	local saved=$current
	run_scene "$key"
	current=$saved
}

run_scene() {
	local key fn
	key=$(scene_key "$1")
	fn=scene_$(printf '%s' "$key" | tr '/-' '__')
	declare -F "$fn" >/dev/null || die "no scene $1"
	current=$key
	log "scene $1"
	"$fn"
	last_done=$key
}

# shell_line types a command on kc's shell line and runs it.
shell_line() {
	typetext "$1"
	sleep 0.3
	key enter
}

# esc sends a lone Esc; it counts once the Esc timeout (1s) passes.
esc() {
	key esc
	sleep 1.2
}

# menu opens a menu of the menu bar (F9): 0 Left, 1 File, 2 Command,
# 3 Options, 4 Right. With a second argument it picks the entry whose
# highlighted letter that is. F9 opens the menu that was open last, Left in
# a new session, and Right steps through the menus in a circle, so menu_at
# keeps track of the last one.
menu_at=0
menu() {
	local i=0 steps=$((($1 - menu_at + 5) % 5))
	key f9
	while [ $i -lt $steps ]; do
		key right
		i=$((i + 1))
	done
	menu_at=$1
	key down
	sleep 0.5
	if [ $# -gt 1 ]; then
		key "$2"
		sleep 0.5
	fi
}

# cx sends the chord C-x and then the key given (a token for key).
cx() {
	key ctrl+x
	key "$1"
}

# meta_after_search presses M-<key> on the row a quick search (search)
# found and waits for the text. A Meta key sent within a second of the
# search was lost, so it waits first; it presses once, since a second press
# acts on the folder the first one opened.
meta_after_search() {
	sleep 2
	meta "$1"
	wait_for "$2" 30
}

SCENES_DIR=$SHOTS/scenes
for f in "$SCENES_DIR"/*.sh; do
	# shellcheck source=/dev/null
	. "$f"
done

# scene_ids prints the scene ids in capture order: the order of the scene
# files, and in each file the order of its functions.
scene_ids() {
	local f group prefix
	for f in "$SCENES_DIR"/*.sh; do
		group=$(basename "$f" .sh)
		group=${group#[0-9][0-9]-}
		prefix=$(printf '%s' "$group" | tr '-' '_')
		sed -n "s/^scene_${prefix}_\\([a-z0-9_]*\\)() *{.*/\\1/p" "$f" | tr '_' '-' | awk -v g="$group" '{ printf "%s/%02d-%s\n", g, NR, $0 }'
	done
}

cmd_capture() {
	start_tunnel
	build_kc
	mirror_kubeconfig
	local_files
	local ids=() sel id
	if [ $# -eq 0 ]; then
		# shellcheck disable=SC2046 # scene ids have no spaces
		set -- $(scene_ids)
	fi
	for sel in "$@"; do
		sel=${sel%.png}
		sel=${sel#screenshots/}
		case $sel in
		*/*) ids+=("$sel") ;;
		*)
			local found=
			for id in $(scene_ids); do
				case $id in "$sel"/*) ids+=("$id"); found=1 ;; esac
			done
			[ -n "$found" ] || die "no scene or group $sel"
			;;
		esac
	done
	for id in "${ids[@]}"; do
		run_scene "$id"
	done
	stop_kc_if_running
	log "captured ${#ids[@]} scenes"
}

case "${1:-all}" in
up) cmd_up ;;
seed) cmd_seed ;;
capture)
	shift
	cmd_capture "$@"
	;;
list) scene_ids ;;
down) cmd_down ;;
all)
	ON_EXIT=cmd_down
	trap cmd_down EXIT
	cmd_up
	cmd_seed
	cmd_capture
	;;
*) die "unknown command $1" ;;
esac
