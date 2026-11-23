# shellcheck shell=bash
# NetworkPolicy between two pods (C-x n): web in the left panel, api in the
# right one.

scene_networkpolicy_result() {
	fresh
	shell_line "cd pods"
	wait_for "web-"
	key tab
	menu 4 l
	shell_line "cd pods"
	sleep 2
	search /api
	key tab
	search /web
	cx n
	wait_for "egress" 30
	widen
	shot 1
}

scene_networkpolicy_policy() {
	after networkpolicy/result
	search api-from-web
	key f3
	wait_for "kind: NetworkPolicy" 30
	shot 1
}
