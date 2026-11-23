# Dashboard

[All screenshots](../README.md)

These images show the redesigned dashboard of [PR #321](https://github.com/stokaro/kc/pull/321). They were captured with kc built from that branch and will be captured again once it merges.

The dashboard of the namespace: its workloads with their usage, share of the request and problems, the pods of the selected workload, CPU and memory over the session with the request and limit lines, and the Events of the range. `C-x g` at the root of a namespace; `+` zooms to the last 5 minutes.

![](01-namespace.png)

A drilldown to one workload: its pods in the list, the charts and Events of the Deployment. `Up`/`Down` to the workload, `Enter`; `Backspace` goes up again.

![](02-workload.png)

The cursor on the CPU chart pauses the live view and shows the values and Events of that cell. `Tab` to the chart, then `Left`.

![](03-cursor.png)

The focused chart on the whole screen. `F4`; `F4` again restores the layout.

![](04-expanded.png)

The dashboard of one pod of the workload. `Space` to go live, `Shift+Tab` to the list, a pod row, `Enter`.

![](05-pod.png)

The Range menu. `F2`.

![](06-range-menu.png)
