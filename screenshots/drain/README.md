# Node drain

[All screenshots](../README.md)

The node actions. `M-n` on a node.

![](01-node-actions.png)

The drain options, with emptyDir data allowed for a pod that keeps data in one. Drain...

![](02-options.png)

The confirmation: the context, what the drain evicts, the PodDisruptionBudgets and the same `kubectl drain` command. Enter.

![](03-confirm.png)

The drain progress once it is done.

![](04-drained.png)

The node stays cordoned afterwards.

![](05-cordoned.png)
