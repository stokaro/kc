# 0279. Open an Event's target by UID

- Status: Accepted
- Date: 2026-11-23
- Issue: #279

## Context

`Enter` on a warning event in the overview opens the involved object in the other panel. The overview rule keeps the whole `involvedObject`, but `involvedObjectRef` copied only GVR, namespace and name into `models.ObjectRef`, and `selectLinkedRow` selected the row whose ID equals the name. A Pod `web` that was deleted and recreated has a new UID, and the event about the old Pod selected the new one. The claim in the issue is correct on current main.

The issue also asks for an explicit action that opens the replacement, a "deleted/replaced state", and a test for forbidden target access.

## Decision

1. `models.ObjectRef` gets an optional `UID`. `involvedObjectRef` copies `involvedObject.uid` into it. Helm release links and other refs leave it empty and keep the name-only behavior.
2. `linkSelectMsg` carries the whole ref instead of the name. For a ref with a UID, `selectLinkedRow` reads the listed row's UID from the folder (`Folder.ItemByID`, the row's `UID()`, which comes from the shared cache) before it selects anything:
   - same UID: select the row, as before;
   - another UID: do not select the row, and show "`<resource> <ns>/<name>` was replaced: the reference is to UID A, the listed object is UID B; its row is not selected";
   - the row has no UID (a list kc cannot read instances from): select it and say the match is unverified;
   - no row yet: retry like before; when the attempts run out, say the object is not in the list and may be deleted or not listable for this user.
3. Focus stays on the overview panel, so the event stays in view for `F3`/`F8`.

## Deviations from the issue

- No separate "open the replacement" command. The list of the right type and namespace is already open in the other panel, with the replacement row in it; moving the cursor there is the explicit action, and the message says both UIDs. A new key for this would be one more binding for a rare case.
- Forbidden access is not tested as its own case. When the type cannot be listed, the list already shows its own access message (see the limited-access envtest suites in `internal/models`); the link adds only the final "not in the list ... may not be allowed to list it" message, which the missing-object case covers.
- The test uses the envtest API server and a real ConfigMap rather than fake objects, because the row UID comes from the shared table cache and a fake list would not prove that path.

## Consequences

- An event never moves the cursor onto a different object instance.
- References without a UID (Helm manifests, the Info panel's top pods) behave as before.
- The UID comparison costs one `ItemByID` lookup per retry; no request is made.
