# shellcheck shell=bash
# Info mode and the cluster overview.

scene_info_workload() {
	fresh
	shell_line "cd deployments"
	wait_for "/loadgen"
	search /loadgen
	wait_for "Top pods of deployments.apps shop/loadgen" 30
	# Let the session history collect a few samples for the sparklines.
	sleep 60
	shot
}

scene_info_node() {
	fresh
	shell_line "cd /nodes"
	wait_for "Roles"
	sleep 2
	key down
	key down
	wait_for "$CLUSTER-worker (v1)"
	sleep 20
	shot
}

scene_info_overview() {
	fresh
	meta i
	wait_for "/attention" 60
	wait_for "/pods image pull err" 60
	sleep 2
	shot
}

scene_info_attention() {
	after info/overview
	enter /att "/overview/attention "
	search "/pods image"
	wait_for "Scope: namespace shop"
	shot 1
}

scene_info_finding() {
	after info/attention
	key enter
	wait_for "ImagePullBackOff"
	shot 1
}

scene_info_custom_workload() {
	custom_workload_up
	fresh --context "$CONTEXT" -n "$CUSTOM_NS"
	shell_line "cd $CUSTOM_CRD"
	wait_for "storefront"
	search storefront
	wait_for "Pods of $CUSTOM_CRD $CUSTOM_NS/storefront" 60
	wait_for "3/3 ready, 3 pods" 60
	# Let the session history collect a few samples for the sparklines.
	sleep 60
	shot
	custom_workload_down
}
