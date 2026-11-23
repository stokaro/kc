# Diagnosis

[All screenshots](../README.md)

`/diagnosis` of a Deployment whose pod cannot pull its image: the findings with their evidence and the next step. Enter `/diagnosis` inside a Deployment.

![](01-workload.png)

`/diagnosis` of a Service whose selector matches no running pod.

![](02-service.png)

`/diagnosis` of a pod whose claim names a StorageClass that does not exist.

![](03-claim.png)

`/diagnosis` of a pod no node can take: the scheduler's message and kc's node checks.

![](04-pending-pod.png)

`/diagnosis` of a crash-looping pod, with rows for the current and the previous log.

![](05-crash-loop.png)
