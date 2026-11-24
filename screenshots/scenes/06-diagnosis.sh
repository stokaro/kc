# shellcheck shell=bash
# /diagnosis of a broken workload, a Service without endpoints, a claim
# that names a missing StorageClass, a pod no node can take and a
# crash-looping pod.

# diagnose opens the /diagnosis row of the object the shell line path names
# and widens the panel.
diagnose() {
	shell_line "cd $1"
	wait_for "/diagnosis" 30
	enter /diag "/diagnosis "
	sleep 2
	widen
}

scene_diagnosis_workload() {
	fresh
	diagnose deploy/checkout
	wait_for "error"
	shot 1
}

scene_diagnosis_service() {
	fresh
	diagnose svc/payments
	wait_for "No running pod match"
	shot 1
}

scene_diagnosis_claim() {
	fresh
	diagnose pvc/uploads
	wait_for "fast-ssd" 30
	shot 1
}

scene_diagnosis_pending_pod() {
	fresh --context "$CONTEXT" -n batch
	diagnose pods/bigjob
	wait_for "nodes are available" 30
	shot 1
}

scene_diagnosis_crash_loop() {
	fresh --context "$CONTEXT" -n batch
	shell_line "cd pods"
	wait_for "report-"
	enter /report "/pods/report-"
	enter /diag "/diagnosis "
	sleep 2
	widen
	wait_for "log"
	shot 1
}
