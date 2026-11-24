# shellcheck shell=bash
# Comparing: two objects (C-x C-d), a local manifest with the live object,
# two lists (C-x d) and a list with another context (C-x c).

# right_list puts the right panel in List mode on the folder the shell line
# path names, and returns to the left panel.
right_list() {
	key tab
	menu 4 l
	shell_line "cd $1"
	sleep 2
}

scene_compare_objects() {
	fresh
	shell_line "cd deployments"
	wait_for "/loadgen"
	right_list deployments
	search /api
	key tab
	search /web
	cx ctrl+d
	wait_for "+++ right/" 90
	shot 1
}

scene_compare_local_file() {
	fresh
	shell_line "cd deployments"
	wait_for "/loadgen"
	right_list deployments
	search /web
	key tab
	meta l
	wait_for "access.log"
	enter /manifests "/manifests"
	wait_for "web.yaml"
	search web
	cx ctrl+d
	wait_for "+++" 90
	shot 1
}

scene_compare_lists() {
	fresh
	shell_line "cd deployments"
	wait_for "/loadgen"
	key tab
	menu 4 l
	shell_line "cd -n staging"
	sleep 2
	shell_line "cd deployments"
	wait_for "/search"
	key tab
	cx d
	wait_for "only right" 90
	shot 1
}

scene_compare_list_pair() {
	after compare/lists
	# The pairs sort by state, then name: changed api, changed web.
	key down
	key enter
	wait_for "+++" 60
	shot 1
}

scene_compare_context_picker() {
	fresh
	shell_line "cd deployments"
	wait_for "/loadgen"
	cx c
	wait_for "$MIRROR"
	shot
}

scene_compare_context_result() {
	after compare/context-picker
	pick "$MIRROR"
	key enter
	wait_for "whole list" 90
	shot 1
}
