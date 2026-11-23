# 0268. Explain volume provisioning, attachment and mount failures

- Status: Accepted
- Date: 2026-11-23
- Issue: #268

## Context

The workload diagnosis reports a claim the pod uses that is missing or
not Bound, and then asks the operator to check the StorageClass, the
provisioner and the binding mode. It says nothing about a Bound claim
whose volume cannot be attached or mounted. Issue #268 asks to follow
claim, StorageClass, PersistentVolume and VolumeAttachment, to tell
provisioning, binding, attachment and reported mount problems apart, to
treat `WaitForFirstConsumer` as expected waiting, and to make forbidden
cluster-scoped reads incomplete, without probes or changes.

## Decision

1. **Where.** A pod's own `/diagnosis` and a new `/diagnosis` inside a
   PersistentVolumeClaim run the storage explanation. A claim's
   diagnosis explains the claim for each pod that uses it (at most five,
   by the volumes that name it), or for the claim alone when no pod uses
   it. A workload's diagnosis reads no cluster-scoped storage objects; it
   keeps the generic "not Bound" warning and gains the mount and attach
   events of its pods, with a hint to open the pod's diagnosis.
2. **Stages**, each a finding with its own code and sources:
   - unbound: a warning event on the claim (`ProvisioningFailed`,
     `FailedBinding`, `VolumeMismatch`, `ClaimLost`) is an error that
     quotes it. Otherwise the class decides: no class, `""` (static
     binding only), a class that does not exist, `WaitForFirstConsumer`
     before the pod is scheduled (info: expected), or waiting for the
     provisioner, with the scheduler's `selected-node` annotation when it
     is set;
   - bound: the PV exists and is not `Failed`; `Lost` claims are errors;
   - attachment, only for CSI volumes of a pod on a node: the
     VolumeAttachment for that PV and node. `status.attachError` is an
     error dated by its own time; not attached yet is info; none at all
     is info when the driver needs one (`attachRequired` true or unset,
     or no CSIDriver object, which Kubernetes treats as required) and
     nothing when `attachRequired: false`. A CSIDriver kc cannot read
     makes the absent attachment `incomplete`;
   - mount: the pod's newest `FailedAttachVolume`, `FailedMount` and
     `FailedMapVolume` events, for a pod that is Pending on its node.
     Older events of a running pod are not reported.
   With the storage input, the generic claim warning is replaced by the
   stage finding, so a claim waiting for its first consumer is not also
   a "not Bound" warning.
3. **Reads.** Through the diagnosis's scoped reader: the StorageClass of
   each unbound claim, the PV of each bound claim, the CSIDriver of each
   CSI PV, and the VolumeAttachments (one cluster-scoped list) only when
   a pod on a node uses a CSI volume. Each object is read once per
   refresh. They are not watched: the claim's status and events, and the
   pod's events, which the folder watches, follow their changes; `C-r`
   reads them again. A forbidden read makes the stage incomplete with
   the API's reason; only a NotFound is "does not exist".

## Deviations from the issue

- **No Node or topology facts in the storage findings.** The scheduling
  explanation (ADR 0262) already checks the node affinity of bound
  volumes for an unschedulable pod; repeating it here would show the
  same fact twice.
- **Events by UID only, newest per reason.** The issue asks for
  UID-matched events; kc cites the newest warning per reason and links
  up to the usual number of events, rather than a full event history.
- **Volume events only for a pod Pending on its node.** Events live for
  an hour; an old `FailedMount` of a pod that now runs would read as a
  current failure.

## Consequences

- A user without access to cluster-scoped storage objects still sees the
  claim and the events, with incomplete stages.
- A cluster-wide VolumeAttachment informer starts the first time a pod's
  diagnosis needs it; attachments are one per attached CSI volume.
- Follow-ups: in-tree volumes migrated to CSI (attachments exist for
  them too, kc checks only `spec.csi`); a claim's diagnosis for a pod in
  another state than Pending that cannot mount after a restart.
