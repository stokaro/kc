# shellcheck shell=bash
# The full-screen dashboard (C-x g): the namespace, a drilldown to a
# workload and a pod, the chart cursor and the Range menu. kc draws the
# samples it collects while a dashboard is open, per target, so the
# namespace waits $DASH_WAIT seconds and each drilldown half of that. The
# charts are Kitty graphics images (kc_config in capture.sh); each shot
# waits until the screen shows them.

# select_workload moves the list selection to the workload whose pods the
# list then shows under the workloads. The list sorts by usage, so the row
# is found by what the screen says, not by its position.
select_workload() {
	for _ in 1 2 3 4 5 6 7 8; do
		key up
	done
	for _ in 1 2 3 4 5 6 7 8; do
		sleep 0.5
		shows "Pods of Deployment $1 (Enter to open)" && return 0
		key down
	done
	die "the dashboard list did not select Deployment $1"
}

scene_dashboard_namespace() {
	fresh
	cx g
	wait_for "namespace shop  LIVE" 30
	# + zooms the session history to its shortest range, 5 minutes.
	key +
	wait_for "last 5 minutes"
	log "collecting dashboard samples for ${DASH_WAIT}s"
	sleep "$DASH_WAIT"
	wait_for_images 2
	shot
}

scene_dashboard_workload() {
	after dashboard/namespace
	select_workload loadgen
	key enter
	wait_for "› Deployment loadgen  LIVE"
	log "collecting workload samples for $((DASH_WAIT / 2))s"
	sleep $((DASH_WAIT / 2))
	wait_for_images 2
	shot
}

scene_dashboard_cursor() {
	after dashboard/workload
	# The focus moves from the list to the CPU chart; Left pauses and puts
	# the cursor on the chart.
	key tab
	for _ in 1 2 3 4 5 6 7 8 9 10; do
		key left
	done
	wait_for "PAUSED"
	wait_for_images 2
	shot
}

scene_dashboard_expanded() {
	after dashboard/cursor
	key f4
	wait_for "4Restore"
	# The expanded chart is a new image, of the new plot size.
	wait_for_images 1
	shot
}

scene_dashboard_pod() {
	after dashboard/expanded
	key f4
	# Space goes live again; Shift+Tab moves the focus back to the list,
	# whose first row is the way up.
	key space
	# The driver has no Shift+Tab token; send its escape sequence.
	bh "text $(printf '\033[Z')"
	sleep 0.5
	key up
	key up
	key up
	key down
	key enter
	wait_for "› pod loadgen-"
	log "collecting pod samples for $((DASH_WAIT / 2))s"
	sleep $((DASH_WAIT / 2))
	wait_for_images 2
	shot
}

scene_dashboard_range_menu() {
	after dashboard/pod
	key f2
	wait_for "Last 15 minutes"
	wait_for_images 2
	shot
	esc
}
