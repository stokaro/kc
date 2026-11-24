# shellcheck shell=bash
# Panels and navigation: the root, contexts and namespaces, object lists,
# what is inside an object, related objects, the hotlist and the history.

scene_panels_root() {
	fresh
	wait_for "Top pods by memory in namespace shop" 60
	wait_for "samples over" 60
	shot
}

scene_panels_contexts() {
	fresh
	menu 0 t
	wait_for "$MIRROR"
	shot
}

scene_panels_namespaces() {
	fresh
	menu 0 n
	wait_for "All namespaces"
	shot
}

scene_panels_pods() {
	fresh
	shell_line "cd pods"
	wait_for "/pods "
	wait_for "loadgen-"
	search loadgen
	widen
	shot 2
}

scene_panels_filter() {
	fresh
	shell_line "cd pods"
	wait_for "loadgen-"
	meta f
	wait_for "Filter"
	typetext "status!=Running"
	shot
}

scene_panels_filtered() {
	after panels/filter
	key enter
	wait_for "kc also filters: status!=Running"
	shot
}

scene_panels_faults() {
	fresh --context "$CONTEXT" -A
	shell_line "cd pods"
	wait_for "kube-system"
	key ctrl+z
	wait_for "bigjob"
	shot
}

scene_panels_marks() {
	fresh
	shell_line "cd deployments"
	wait_for "/loadgen"
	bh "text +"
	wait_for "Select group"
	typetext "*a*"
	key enter
	sleep 1
	shot
}

scene_panels_deployment() {
	fresh
	shell_line "cd deploy/web"
	wait_for "/rollout"
	shot
}

scene_panels_pod() {
	fresh
	shell_line "cd pods"
	wait_for "web-"
	enter /web "/pods/web-"
	wait_for "/containers"
	shot
}

scene_panels_container_files() {
	after panels/pod
	enter /cont "/containers "
	wait_for "/nginx"
	enter /nginx "/containers/nginx "
	enter /root "/nginx/root "
	wait_for "docker-entrypoint"
	enter etc "/root/etc "
	wait_for "│nginx "
	enter nginx "/root/etc/nginx "
	wait_for "nginx.conf"
	search nginx.c
	shot
}

scene_panels_owners() {
	fresh
	shell_line "cd pods"
	wait_for "web-"
	search /web
	meta_after_search j "/owners"
	shot 1
}

scene_panels_used_by() {
	fresh
	shell_line "cd configmaps"
	wait_for "/web-config"
	search /web-config
	meta_after_search b "/used-by"
	widen
	shot 1
}

scene_panels_requires() {
	fresh
	shell_line "cd pods"
	wait_for "web-"
	search /web
	meta_after_search d "/requires"
	widen
	shot 1
}

scene_panels_explain() {
	fresh
	shell_line "cd deployments"
	wait_for "/loadgen"
	search /web
	meta_after_search e "spec"
	widen
	shot 1
}

scene_panels_access() {
	fresh
	shell_line "cd pods"
	wait_for "web-"
	search /web
	meta_after_search k "can-i"
	shot 1
}

scene_panels_hotlist() {
	fresh
	key 0x1c
	wait_for "not running"
	shot
}

scene_panels_saved_view() {
	after panels/hotlist
	key enter
	wait_for "kc also filters: status!=Running"
	wait_for "/checkout-"
	shot 1
}

scene_panels_history() {
	fresh
	# Enter, not cd on the shell line: the history did not record a cd.
	enter /deployments "/deployments "
	wait_for "/loadgen"
	enter /web "/deployments/web "
	enter /replicasets "/web/replicasets "
	key home
	key enter
	wait_for "/rollout"
	key home
	key enter
	wait_for "/loadgen"
	key home
	key enter
	wait_for "/servicecidrs"
	enter /nodes "/nodes "
	wait_for "Roles"
	# The Info panel of a cluster-scoped list reads all namespaces.
	wait_for "Top pods by memory in all namespaces" 30
	meta H
	wait_for "current"
	shot
}

scene_panels_helm() {
	fresh
	shell_line "cd /helm"
	wait_for "banner"
	shot 1
}

scene_panels_helm_release() {
	after panels/helm
	enter banner "/helm/banner"
	wait_for "/history"
	shot 1
}

scene_panels_port_forward() {
	fresh
	shell_line "cd pods"
	wait_for "web-"
	search /web
	meta_after_search p "Port forward"
	shot
}
