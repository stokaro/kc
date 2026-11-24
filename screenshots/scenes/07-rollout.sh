# shellcheck shell=bash
# /rollout of a Deployment with three revisions, Events of an object, the
# /timeline kc records while it runs, and the Scale dialog.

scene_rollout_revisions() {
	fresh
	shell_line "cd deploy/catalog"
	wait_for "/rollout"
	enter /rollout "/catalog/rollout "
	wait_for "complete" 30
	widen
	shot 1
}

scene_rollout_template_diff() {
	after rollout/revisions
	search " changes"
	key f3
	wait_for "MODE" 30
	shot 1
}

scene_rollout_events() {
	fresh
	shell_line "cd pods"
	wait_for "checkout-"
	enter /checkout "/pods/checkout-"
	enter /events "/events "
	wait_for "BackOff" 30
	widen
	shot 1
}

scene_rollout_timeline() {
	# The scale-up below is undone at the start, so the scene can run again.
	kk kubectl -n shop scale deploy/web --replicas=2 >/dev/null
	fresh
	shell_line "cd deploy/web"
	wait_for "/rollout"
	enter /tim "/deployments/web/timeline "
	# The column shows "first observed re…" until widen.
	wait_for "first observed re" 30
	widen
	kk kubectl -n shop scale deploy/web --replicas=3 >/dev/null
	wait_for "spec.replicas" 60
	sleep 3
	shot 1
}

scene_rollout_timeline_diff() {
	after rollout/timeline
	# Rows are named by time, newest first. From the end: stop recording,
	# the note, the ReplicaSet, the initial revision, then the spec change.
	key end
	key up
	key up
	key up
	key up
	sleep 0.5
	key f3
	wait_for "replicas: 3" 30
	shot 1
	key f3
	kk kubectl -n shop scale deploy/web --replicas=2 >/dev/null
}

scene_rollout_scale_dialog() {
	fresh
	shell_line "cd deploy/web"
	wait_for "/rollout"
	enter " Scale" "Scale Deployment"
	shot
	esc
}
