# shellcheck shell=bash
# Node actions (M-n): drain kc-shots-worker2 with progress, then uncordon it.

scene_drain_node_actions() {
	# An earlier run may have left the node cordoned.
	kk kubectl uncordon "$CLUSTER-worker2" >/dev/null
	fresh
	shell_line "cd /nodes"
	wait_for "Roles"
	sleep 2
	key down
	key down
	key down
	wait_for "$CLUSTER-worker2 (v1)"
	node_actions
	shot
}

scene_drain_options() {
	after drain/node-actions
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
	shot
}

scene_drain_confirm() {
	after drain/options
	key enter
	wait_for "Drain node?"
	shot
}

scene_drain_drained() {
	after drain/confirm
	key left
	key enter
	wait_for "Drained node" 300
	shot
}

scene_drain_cordoned() {
	after drain/drained
	key enter
	wait_for "SchedulingDisabled"
	shot 1
	kk kubectl uncordon "$CLUSTER-worker2" >/dev/null
}
