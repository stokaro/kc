# shellcheck shell=bash
# Metric sources (C-x m): what the context serves, and the Prometheus
# backend. The cluster has metrics-server and no Prometheus.

scene_metrics_sources() {
	fresh
	cx m
	wait_for "metrics.k8s.io" 30
	sleep 3
	shot
}
