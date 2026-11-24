# shellcheck shell=bash
# Viewers and editing: YAML and search, the Describe, Manifest and Logs panel
# modes, merged JSON logs with a filter, local files with the paged viewer
# and F4, and the copy dialog.

scene_viewer_yaml() {
	fresh
	shell_line "cd deployments"
	wait_for "/loadgen"
	search /web
	key f3
	wait_for "kind: Deployment"
	shot
}

scene_viewer_search() {
	after viewer/yaml
	key f7
	wait_for "Search"
	typetext "replicas"
	shot
}

scene_viewer_describe_mode() {
	fresh
	shell_line "cd deployments"
	wait_for "/loadgen"
	search /web
	menu 4 d
	wait_for "Selector:" 30
	shot 1
}

scene_viewer_manifest_mode() {
	after viewer/describe-mode
	menu 4 m
	wait_for "apiVersion: apps/v1" 30
	shot 1
}

scene_viewer_logs_mode() {
	fresh
	shell_line "cd pods"
	wait_for "api-"
	search /api
	menu 4 g
	wait_for "payment declined" 30
	shot 2
}

scene_viewer_merged_logs() {
	fresh
	shell_line "cd deploy/api"
	wait_for "/rollout"
	search " logs"
	key f3
	wait_for "payment declined" 30
	shot 2
}

scene_viewer_log_filter() {
	after viewer/merged-logs
	meta g
	wait_for "Log filter"
	typetext "level=error,warn"
	shot
}

scene_viewer_log_filtered() {
	after viewer/log-filter
	key enter
	sleep 1.5
	shot
}

scene_viewer_log_fields() {
	after viewer/log-filtered
	meta d
	sleep 1.5
	shot
}

scene_viewer_local_panel() {
	fresh
	meta l
	wait_for "access.log"
	shot
}

scene_viewer_local_file() {
	after viewer/local-panel
	search notes
	key f3
	wait_for "on-call notes"
	shot
}

scene_viewer_paged() {
	fresh
	meta l
	wait_for "access.log"
	search access
	key f3
	wait_for "paged" 30
	key end
	sleep 2
	shot
}

scene_viewer_local_edit() {
	fresh
	meta l
	wait_for "access.log"
	search notes
	key f4
	wait_for "crash loops"
	shot 1
	typetext ":q!"
	key enter
	wait_for "access.log"
}

scene_viewer_copy() {
	fresh
	shell_line "cd deployments"
	wait_for "/loadgen"
	search /web
	key f5
	wait_for "to:"
	shot
	esc
}
