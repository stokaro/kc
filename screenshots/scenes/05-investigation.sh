# shellcheck shell=bash
# The investigation window (C-x w): one time window for the Logs and Info
# panels and the dashboard.

scene_investigation_dialog() {
	fresh
	shell_line "cd pods"
	wait_for "api-"
	search /api
	cx w
	wait_for "time zone"
	typetext 10m
	shot
}

scene_investigation_logs() {
	after investigation/dialog
	key enter
	sleep 1
	menu 4 g
	wait_for "window" 30
	wait_for "payment declined" 30
	shot 2
}

scene_investigation_command_menu() {
	after investigation/logs
	menu 2
	wait_for "Investigation window"
	shot
}
