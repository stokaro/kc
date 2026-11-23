# shellcheck shell=bash
# The full-screen dashboard (C-x g) of a Deployment. kc draws the session
# samples it collects while the dashboard is open, so the first scene waits
# $DASH_WAIT seconds.
#
# The charts are drawn with blocks (b): the driver renders PNGs with Go
# Mono, which has no braille glyphs, so the default braille charts come out
# empty. + zooms the session history to its shortest range, 5m.

scene_dashboard_live() {
	fresh
	shell_line "cd deployments"
	wait_for "/loadgen"
	search /loadgen
	cx g
	wait_for "LIVE" 30
	key b
	key +
	log "collecting dashboard samples for ${DASH_WAIT}s"
	sleep "$DASH_WAIT"
	shot
}

scene_dashboard_cursor() {
	after dashboard/live
	for _ in 1 2 3 4 5 6 7 8 9 10 11 12; do
		key left
	done
	wait_for "PAUSED"
	shot
}

scene_dashboard_expanded() {
	after dashboard/cursor
	key enter
	sleep 1
	shot
}

scene_dashboard_events() {
	after dashboard/expanded
	esc
	esc
	key tab
	key tab
	sleep 1
	shot
}

scene_dashboard_pod() {
	after dashboard/events
	key tab
	sleep 0.5
	key enter
	sleep 2
	shot
}
