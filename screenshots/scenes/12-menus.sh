# shellcheck shell=bash
# The menu bar (F9), the user menu (F2), dialogs and the F1 help.

scene_menus_left() {
	fresh
	menu 0
	wait_for "Local files"
	shot
}

scene_menus_file() {
	fresh
	shell_line "cd pods"
	wait_for "web-"
	search /web
	menu 1
	wait_for "Quick search"
	shot
}

scene_menus_command() {
	fresh
	menu 2
	wait_for "Investigation window"
	shot
}

scene_menus_options() {
	fresh
	menu 3
	wait_for "Menu bar visible"
	shot
}

scene_menus_user_menu() {
	fresh
	shell_line "cd pods"
	wait_for "web-"
	search /web
	key f2
	wait_for "Top Pods"
	shot
}

scene_menus_panel_options() {
	fresh
	shell_line "cd pods"
	wait_for "web-"
	menu 3 l
	wait_for "Panel mode"
	shot
}

scene_menus_delete_confirm() {
	fresh
	shell_line "cd pods"
	wait_for "web-"
	search /web
	key f8
	wait_for "Cancel"
	shot
	# Cancel has the focus; Esc closes the dialog without deleting.
	esc
}

scene_menus_create_namespace() {
	fresh
	shell_line "cd /namespaces"
	wait_for "/staging"
	# The Info panel of a cluster-scoped list reads all namespaces.
	wait_for "Top pods by memory in all namespaces" 30
	wait_gone "loading" 30
	key f7
	wait_for "namespace"
	shot
	esc
}

scene_menus_help_folder() {
	fresh
	shell_line "cd deploy/checkout"
	wait_for "/diagnosis"
	search /diag
	key f1
	wait_for "How to read it"
	shot
}

scene_menus_help_index() {
	after menus/help-folder
	key f2
	wait_for "Moving around"
	shot
}

scene_menus_help_dialog() {
	fresh
	cx w
	wait_for "time zone"
	key f1
	wait_for "What the field accepts"
	shot
}
