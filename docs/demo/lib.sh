# shellcheck shell=bash
# Shared functions of docs/demo/record.sh and screenshots/capture.sh: a
# throwaway kind cluster (local, or on a Docker host over ssh), the kubectl
# guard kk, and the functions that drive kc through cmd/bubbleheadless.
#
# Source it from bash after setting:
#   LIB_TAG      prefix of log lines, such as demo
#   CLUSTER      kind cluster name; the context is kind-$CLUSTER
#   PORT         API server port on 127.0.0.1
#   REPO         repository root
#   WORK         work directory (kubeconfig, temporary HOME, binaries)
#   SSH_DEST     ssh destination of a Linux Docker host, or empty for local Docker
#   REMOTE_KIND  local path of a Linux kind binary to upload when the host has none
#   COLS, ROWS   terminal size kc runs in
#   LOCAL        directory kc starts in
# Optional:
#   WORKERS      number of kind worker nodes (default 1)
#   KC_KUBECONFIG  kubeconfig list kc starts with (default the cluster's own)
#   ON_EXIT      command the EXIT trap runs after kc is stopped

CONTEXT=kind-$CLUSTER
WORK=${WORK%/}
REMOTE_DIR=$CLUSTER-work
WORKERS=${WORKERS:-1}
KCFG=$WORK/kubeconfig
BH_LOG=$WORK/bh.log
BH_FIFO=$WORK/bh.fifo
METRICS_SERVER=https://github.com/kubernetes-sigs/metrics-server/releases/download/v0.8.0/components.yaml
mkdir -p "$WORK/home"

marker_no=0
bh_pid=

log() { printf '[%s] %s\n' "$LIB_TAG" "$*" >&2; }
die() { log "error: $*"; exit 1; }

remote() { ssh -o BatchMode=yes "$SSH_DEST" "$@"; }

# kk runs a command with the throwaway HOME and KUBECONFIG, after checking that
# the kubeconfig points at the throwaway cluster on the local port and nothing
# else.
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
EOF
	local i=0
	while [ $i -lt "$WORKERS" ]; do
		echo "  - role: worker"
		i=$((i + 1))
	done
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

cluster_up() {
	if [ -z "$SSH_DEST" ]; then
		kind_config >"$WORK/kind.yaml"
		HOME=$WORK/home kind create cluster --name "$CLUSTER" --config "$WORK/kind.yaml" --kubeconfig "$KCFG" --wait 120s
	else
		remote "mkdir -p $REMOTE_DIR"
		local rkind=kind
		if ! remote "command -v kind" >/dev/null; then
			[ -n "$REMOTE_KIND" ] || die "no kind on $SSH_DEST; set the remote kind binary"
			remote "cat > $REMOTE_DIR/kind && chmod +x $REMOTE_DIR/kind" <"$REMOTE_KIND"
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

cluster_down() {
	if [ -z "$SSH_DEST" ]; then
		HOME=$WORK/home kind delete cluster --name "$CLUSTER" --kubeconfig "$KCFG" || true
	else
		local rkind=kind
		[ -f "$WORK/remote-kind" ] && rkind=$(cat "$WORK/remote-kind")
		remote "$rkind delete cluster --name $CLUSTER --kubeconfig $REMOTE_DIR/kubeconfig; rm -rf $REMOTE_DIR" || true
		stop_tunnel
	fi
}

# seed_metrics_server installs metrics-server, which feeds the Info panel,
# the CPU and memory columns, the overview's capacity rows and the
# dashboard. kind's kubelets serve self-signed certificates. It runs on the
# control plane, so a drain of a worker neither stops it nor has to delete
# its emptyDir. --force-conflicts takes the args back from an earlier patch,
# which the guard below then adds again.
seed_metrics_server() {
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
	kk kubectl -n kube-system rollout status deploy/metrics-server --timeout=180s
}

# --- driving kc ---------------------------------------------------------------

build_kc() {
	mkdir -p "$WORK/bin"
	(cd "$REPO" && go build -o "$WORK/bin/kc" ./cmd/kc && go build -o "$WORK/bin/bubbleheadless" ./cmd/bubbleheadless)
}

# start_kc starts kc in the driver, in $LOCAL, with the arguments given. The
# environment is empty apart from what kc needs, and PS1 keeps user and host
# names out of the integrated terminal. Set KC_EDITOR for the editor F4 runs
# on a local file.
start_kc() {
	rm -f "$BH_FIFO" "$BH_LOG"
	mkfifo "$BH_FIFO"
	# Assert the target once more, then start kc on the throwaway kubeconfig only.
	kk true
	(cd "$LOCAL" && env -i PATH="$PATH" HOME="$WORK/home" KUBECONFIG="${KC_KUBECONFIG:-$KCFG}" TZ=UTC LANG=en_US.UTF-8 \
		SHELL=/bin/sh PS1='$ ' PWD="$LOCAL" EDITOR="${KC_EDITOR:-vi}" \
		"$WORK/bin/bubbleheadless" -cols "$COLS" -rows "$ROWS" -- "$WORK/bin/kc" "$@" <"$BH_FIFO" >"$BH_LOG" 2>&1) &
	bh_pid=$!
	exec 3>"$BH_FIFO"
	trap 'bh exit 2>/dev/null || true; exec 3>&-; kill '"$bh_pid"' 2>/dev/null || true; '"${ON_EXIT:-:}" EXIT
}

stop_kc() {
	bh exit 2>/dev/null || true
	exec 3>&-
	wait "$bh_pid" 2>/dev/null || true
	if [ -n "${ON_EXIT:-}" ]; then
		# shellcheck disable=SC2064 # ON_EXIT names a function; expand it now
		trap "$ON_EXIT" EXIT
	else
		trap - EXIT
	fi
}

bh() { printf '%s\n' "$*" >&3; }
key() { bh "key $1"; }
meta() { bh "text $(printf '\033')$1"; }
typetext() { bh "text $1"; }

# caption sets the caption the driver draws over the following color
# snapshots; without text it clears it.
caption() { bh "caption $*"; }

# ask sends a driver command and prints what the driver answers.
ask() {
	marker_no=$((marker_no + 1))
	local mark="$LIB_TAG-mark-$marker_no" start
	start=$(wc -c <"$BH_LOG")
	bh "$1"
	bh "$mark"
	# The driver answers an unknown command with its name, which marks the
	# end of the answer.
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

# screen prints the current screen as plain text.
screen() { ask "screen plain"; }

# wait_for_images waits until the screen shows at least the given number of
# Kitty graphics images (default 1), each sent at the cell size of the
# snapshot. kc shows a chart as text until its image has been sent, and
# sizes the image after the driver's answer to its cell size query; a
# scaled image means kc drew it before the answer.
wait_for_images() {
	local want=${1:-1} timeout=${2:-30} start out
	start=$(date +%s)
	while :; do
		out=$(ask images)
		if printf '%s\n' "$out" | awk -v want="$want" '
			/image [0-9]+: .*, shown$/ { n++; if (/, scaled,/) scaled++ }
			END { exit !(n >= want && scaled == 0) }'; then
			return 0
		fi
		if [ $(($(date +%s) - start)) -ge "$timeout" ]; then
			printf '%s\n' "$out" >&2
			die "timed out after ${timeout}s waiting for $want images on the screen"
		fi
		sleep 0.3
	done
}

# settle_images waits until the list of Kitty graphics images, and which of
# them the screen shows, stays the same between two looks 0.4s apart, for
# at most the given seconds (default 5). kc draws an Info bar or a chart as
# text until its image arrives, so a snapshot taken while one is on its way
# mixes text and pictures.
settle_images() {
	local timeout=${1:-5} start prev=none out
	start=$(date +%s)
	while :; do
		out=$(ask images | grep -E 'image [0-9]+:|no images' || true)
		[ "$out" != "$prev" ] || return 0
		prev=$out
		[ $(($(date +%s) - start)) -lt "$timeout" ] || return 0
		sleep 0.4
	done
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

# wait_gone waits until the screen no longer shows the text (fixed string)
# or the timeout in seconds runs out, then fails the script.
wait_gone() {
	local text=$1 timeout=${2:-20} start
	start=$(date +%s)
	while shows "$text"; do
		if [ $(($(date +%s) - start)) -ge "$timeout" ]; then
			screen >&2
			die "timed out after ${timeout}s waiting for \"$text\" to leave the screen"
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

# write_png asks the driver for a color snapshot and waits until the file is
# written.
write_png() {
	local file=$1 i=0
	rm -f "$file"
	bh "snapshot $file color"
	while [ ! -s "$file" ]; do
		i=$((i + 1))
		[ $i -lt 100 ] || die "snapshot $file was not written"
		sleep 0.05
	done
}

# search moves the cursor to the first row that starts with the text (C-s).
search() {
	key ctrl+s
	typetext "$1"
	sleep 0.3
	# A lone Esc counts once the Esc timeout (1s) passes: Esc+key is Meta.
	key esc
	sleep 1.2
}

# pick filters an open picker (the context picker, C-x c) down to the rows
# that contain the text, so Enter takes the first of them. search does not
# work there: the picker filters as you type, and Esc clears the filter.
pick() {
	typetext "$1"
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

# node_actions opens the node menu (M-n) on the node under the cursor. For
# up to a minute after a log stream or a container file session ended, M-n
# answered "Select a node first" in the recordings, so it tries again.
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
