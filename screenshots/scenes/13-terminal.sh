# shellcheck shell=bash
# The integrated terminal (C-o) and a shell in a container (M-x).

scene_terminal_terminal() {
	fresh
	key ctrl+o
	wait_for "Return to panels"
	typetext "kubectl get deploy,pods"
	key enter
	wait_for "deployment.apps/web" 30
	shot 1
}

scene_terminal_shell_line() {
	fresh
	typetext "cd deploy/web"
	shot
}

scene_terminal_container_shell() {
	fresh
	shell_line "cd pods"
	wait_for "web-"
	search /web
	meta_after_search x "kubectl exec"
	sleep 2
	typetext "ls /etc/nginx"
	key enter
	wait_for "nginx.conf" 30
	shot 1
	typetext "exit"
	key enter
	wait_for "/pods " 30
}
