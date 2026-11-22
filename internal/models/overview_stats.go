package models

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	// overviewStatsEvery spaces two reads of the kubelet stats summary. The
	// kubelet refreshes filesystem stats about once a minute.
	overviewStatsEvery = time.Minute
	// overviewStatsInFlight bounds the stats requests sent at the same time.
	overviewStatsInFlight = 5
	// overviewStatsTimeout bounds one node's stats request.
	overviewStatsTimeout = 10 * time.Second
)

// fsUse is the used and total bytes of one filesystem.
type fsUse struct {
	used, capacity int64
}

func (u fsUse) known() bool { return u.capacity > 0 }

func (u fsUse) percent() float64 { return percent(u.used, u.capacity) }

// volumeUse is the use of one mounted PersistentVolumeClaim.
type volumeUse struct {
	namespace, claim string
	fsUse
}

// nodeStats is what the overview reads from one node's stats summary.
type nodeStats struct {
	nodeFS  fsUse
	imageFS fsUse
	volumes []volumeUse
}

// statsFetcher reads one node's stats summary. Tests replace it.
type statsFetcher func(ctx context.Context, node string) (*nodeStats, error)

// kubeletSummary is the part of the kubelet's /stats/summary the overview
// reads (k8s.io/kubelet/pkg/apis/stats/v1alpha1, not a dependency of kc).
type kubeletSummary struct {
	Node struct {
		Fs      *kubeletFsStats `json:"fs"`
		Runtime *struct {
			ImageFs *kubeletFsStats `json:"imageFs"`
		} `json:"runtime"`
	} `json:"node"`
	Pods []struct {
		PodRef struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"podRef"`
		Volume []struct {
			kubeletFsStats
			Name   string `json:"name"`
			PVCRef *struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"pvcRef"`
		} `json:"volume"`
	} `json:"pods"`
}

type kubeletFsStats struct {
	CapacityBytes *uint64 `json:"capacityBytes"`
	UsedBytes     *uint64 `json:"usedBytes"`
}

func (s *kubeletFsStats) use() fsUse {
	if s == nil || s.CapacityBytes == nil || s.UsedBytes == nil {
		return fsUse{}
	}
	return fsUse{used: int64(*s.UsedBytes), capacity: int64(*s.CapacityBytes)}
}

// decodeKubeletSummary reads the filesystems and the claim volumes of a
// stats summary.
func decodeKubeletSummary(raw []byte) (*nodeStats, error) {
	var sum kubeletSummary
	if err := json.Unmarshal(raw, &sum); err != nil {
		return nil, fmt.Errorf("decode stats summary: %w", err)
	}
	out := &nodeStats{nodeFS: sum.Node.Fs.use()}
	if sum.Node.Runtime != nil {
		out.imageFS = sum.Node.Runtime.ImageFs.use()
	}
	for _, p := range sum.Pods {
		for _, v := range p.Volume {
			if v.PVCRef == nil {
				continue
			}
			u := v.use()
			if !u.known() {
				continue
			}
			out.volumes = append(out.volumes, volumeUse{namespace: v.PVCRef.Namespace, claim: v.PVCRef.Name, fsUse: u})
		}
	}
	return out, nil
}

// newKubeletStatsFetcher reads stats summaries through the API server's node
// proxy (GET /api/v1/nodes/<node>/proxy/stats/summary), which needs get on
// nodes/proxy.
func newKubeletStatsFetcher(cfg *rest.Config) (statsFetcher, error) {
	cs, err := kubernetes.NewForConfig(rest.CopyConfig(cfg))
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, node string) (*nodeStats, error) {
		log := ctrllog.FromContext(ctx).WithName("overview")
		log.Info("client get", "gvr", "v1/nodes/proxy", "name", node, "path", "stats/summary")
		raw, err := cs.CoreV1().RESTClient().Get().Resource("nodes").Name(node).SubResource("proxy").Suffix("stats", "summary").DoRaw(ctx)
		if err != nil {
			return nil, err
		}
		return decodeKubeletSummary(raw)
	}, nil
}

// statsResult is one round of stats requests.
type statsResult struct {
	stats  map[string]*nodeStats
	errors map[string]string
	reason string // set when no stats are known at all
}

// pollNodeStats reads the stats of nodes, at most overviewStatsInFlight at a
// time. It does not read them on a cluster with more than maxNodes nodes.
// Forbidden on every node means the identity may not use nodes/proxy.
func pollNodeStats(ctx context.Context, fetch statsFetcher, nodes []string, maxNodes int) statsResult {
	res := statsResult{stats: map[string]*nodeStats{}, errors: map[string]string{}}
	switch {
	case fetch == nil:
		res.reason = "no cluster connection"
		return res
	case len(nodes) == 0:
		res.reason = "no nodes"
		return res
	case len(nodes) > maxNodes:
		res.reason = fmt.Sprintf("skipped: %d nodes, more than statsMaxNodes (%d)", len(nodes), maxNodes)
		return res
	}
	var (
		mu        sync.Mutex
		wg        sync.WaitGroup
		forbidden int
		firstErr  string
	)
	sem := make(chan struct{}, overviewStatsInFlight)
	for _, node := range nodes {
		wg.Go(func() {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			nctx, cancel := context.WithTimeout(ctx, overviewStatsTimeout)
			s, err := fetch(nctx, node)
			cancel()
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				msg := err.Error()
				if apierrors.IsForbidden(err) {
					forbidden++
					msg = "forbidden: nodes/proxy"
				}
				res.errors[node] = msg
				if firstErr == "" {
					firstErr = msg
				}
				return
			}
			res.stats[node] = s
		})
	}
	wg.Wait()
	if len(res.stats) == 0 {
		switch {
		case forbidden == len(nodes):
			res.reason = "forbidden: nodes/proxy (get on nodes/proxy is needed for the kubelet stats)"
		case firstErr != "":
			res.reason = "kubelet stats: " + firstErr
		default:
			res.reason = "kubelet stats: no answer"
		}
	}
	return res
}

// formatGiB prints a size in GiB: one decimal below 10 GiB.
func formatGiB(b int64) string {
	g := float64(b) / (1 << 30)
	if g < 10 {
		return fmt.Sprintf("%.1f", g)
	}
	return fmt.Sprintf("%.0f", g)
}

func formatUse(u fsUse) string {
	return fmt.Sprintf("%.0f%% (%s/%sGi)", u.percent(), formatGiB(u.used), formatGiB(u.capacity))
}

// statsUnavailable marks f as dim when no kubelet stats are known.
func statsUnavailable(rc *ruleContext, f *overviewFinding) bool {
	reason := rc.in.missingReason(ovNodesGVR)
	if reason == "" && len(rc.m.stats) == 0 {
		reason = rc.m.statsReason
		if reason == "" {
			reason = "loading"
		}
	}
	if reason == "" {
		return false
	}
	f.unavailable, f.value, f.state, f.severity = reason, "n/a", severityUnknown.String(), severityUnknown
	return true
}

func capacityDiskRule(rc *ruleContext) overviewFinding {
	f := overviewFinding{
		id: "capacity/disk", section: overviewCapacity, title: "/nodes disk",
		explain: fmt.Sprintf(`The used bytes of each node's filesystem (nodefs) and, when the container
runtime keeps images on another filesystem, of its image filesystem
(imagefs), from the kubelet stats summary
(/api/v1/nodes/<node>/proxy/stats/summary, read once a minute while the
overview is open, at most %d requests at a time). They are summed over the
nodes. This needs get on nodes/proxy; a cluster with more than
statsMaxNodes (%d) nodes is not read.

A node is "full" when a filesystem reaches diskFailPercent (%d%%) and
"high" when it reaches diskWarnPercent (%d%%). The kubelet evicts pods
when a filesystem runs low. Enter lists the nodes, the fullest first.`,
			overviewStatsInFlight, rc.cfg.StatsMaxNodes, rc.cfg.DiskFailPercent, rc.cfg.DiskWarnPercent),
		kubectl: [][]string{{"get", "--raw", "/api/v1/nodes/<node>/proxy/stats/summary"}},
	}
	if statsUnavailable(rc, &f) {
		return f
	}
	var sum fsUse
	worst, worstFlag := severityOK, "ok"
	for i := range rc.in.nodes {
		n := &rc.in.nodes[i]
		s := rc.m.stats[n.Name]
		if s == nil {
			value := "no stats"
			if msg := rc.m.statsErrors[n.Name]; msg != "" {
				value += ": " + msg
			}
			f.targets = append(f.targets, overviewTarget{gvr: ovNodesGVR, name: n.Name, value: value, severity: severityUnknown})
			continue
		}
		parts := []string{}
		pct := 0.0
		for _, fs := range []struct {
			label string
			use   fsUse
		}{{"nodefs", s.nodeFS}, {"imagefs", separateImageFS(s)}} {
			if !fs.use.known() {
				continue
			}
			sum.used += fs.use.used
			sum.capacity += fs.use.capacity
			parts = append(parts, fs.label+" "+formatUse(fs.use))
			pct = max(pct, fs.use.percent())
		}
		sev, flag := severityOK, "ok"
		switch {
		case pct >= float64(rc.cfg.DiskFailPercent):
			sev, flag = severityFailed, "full"
		case pct >= float64(rc.cfg.DiskWarnPercent):
			sev, flag = severityWarning, "high"
		}
		if sev > worst {
			worst, worstFlag = sev, flag
		}
		if len(parts) == 0 {
			parts = append(parts, "no filesystem stats")
		}
		f.targets = append(f.targets, overviewTarget{
			gvr: ovNodesGVR, name: n.Name, severity: sev, rank: pct,
			value: strings.Join(parts, " ") + flagSuffix(flag),
		})
	}
	f.targets = sortTargets(f.targets)
	f.value = "n/a"
	if sum.known() {
		f.value = formatUse(sum)
	}
	f.severity, f.state = worst, worstFlag
	return f
}

// separateImageFS returns the image filesystem when it is not the node
// filesystem; a runtime that keeps images on the root filesystem reports
// the same capacity for both.
func separateImageFS(s *nodeStats) fsUse {
	if s.imageFS.capacity == s.nodeFS.capacity {
		return fsUse{}
	}
	return s.imageFS
}

func volumesNearFullRule(rc *ruleContext) overviewFinding {
	f := overviewFinding{
		id: "risks/volumes-full", section: overviewRisks, title: "/pvcs near full",
		explain: fmt.Sprintf(`PersistentVolumeClaims mounted by a running pod whose used bytes reach
volumeWarnPercent (%d%%) of the capacity the kubelet reports for the
volume (kubelet stats summary, see "/nodes disk"). A full volume fails the
row. Some provisioners (local-path, hostPath) report the node filesystem
as the volume's capacity, so their percentages say little.`, rc.cfg.VolumeWarnPercent),
		kubectl: [][]string{
			{"get", "--raw", "/api/v1/nodes/<node>/proxy/stats/summary"},
			kubectlGet("persistentvolumeclaims", rc.nsArgs()...),
		},
	}
	if statsUnavailable(rc, &f) {
		return f
	}
	return countFinding(rc, f, []schema.GroupVersionResource{ovNodesGVR}, func() []overviewTarget {
		seen := map[string]bool{}
		var out []overviewTarget
		nodes := make([]string, 0, len(rc.m.stats))
		for n := range rc.m.stats {
			nodes = append(nodes, n)
		}
		sort.Strings(nodes)
		for _, n := range nodes {
			for _, v := range rc.m.stats[n].volumes {
				key := v.namespace + "/" + v.claim
				if seen[key] || !rc.in.inScope(v.namespace) {
					continue
				}
				seen[key] = true
				pct := v.percent()
				if pct < float64(rc.cfg.VolumeWarnPercent) {
					continue
				}
				sev := severityWarning
				if v.used >= v.capacity {
					sev = severityFailed
				}
				out = append(out, overviewTarget{
					gvr: ovPVCsGVR, namespace: v.namespace, name: v.claim, severity: sev, rank: pct,
					value: formatUse(v.fsUse),
				})
			}
		}
		return out
	})
}

// nodeNames returns the names of nodes.
func nodeNames(nodes []corev1.Node) []string {
	out := make([]string, 0, len(nodes))
	for i := range nodes {
		out = append(out, nodes[i].Name)
	}
	return out
}
