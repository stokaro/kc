# Rollout, Events and timeline

[All screenshots](../README.md)

`/rollout` of a Deployment with three revisions and what changed between them. Enter `/rollout` inside a Deployment.

![](01-revisions.png)

The pod template diff between two revisions. `F3` on a `changes` row.

![](02-template-diff.png)

`/events` of a pod.

![](03-events.png)

`/timeline` of a Deployment: the changes kc observed while it records, here a scale from 2 to 3 replicas.

![](04-timeline.png)

The diff of one timeline entry. `F3` on an entry.

![](05-timeline-diff.png)

The Scale dialog. Enter on `Scale...` inside a Deployment.

![](06-scale-dialog.png)
