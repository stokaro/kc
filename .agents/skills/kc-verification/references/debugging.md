# Investigate a kc failure

Read this only when the cause remains unclear, the failure is intermittent or a
previous fix did not resolve it. Keep the investigation scoped to the symptom.

1. **Capture the failure.** Keep the exact command, relevant output, environment
   and code revision. Reproduce with the smallest test/package or isolated
   envtest fixture. If it will not reproduce, gather evidence before editing.
2. **Trace the owner and inputs.** Follow the failing value or event back to its
   producer. For asynchronous UI work, record the originating panel, connection,
   request identity and cancellation path. For cache/RBAC failures, distinguish
   API state from informer state and authorization propagation. Read the
   relevant project skill or ADR instead of all architecture documents.
3. **State one hypothesis.** Identify the cause, supporting observation and a
   check that could disprove it. Compare a nearby working implementation. Add
   temporary diagnostics only at the boundary needed to distinguish causes;
   never print credentials, kubeconfigs or Secret payloads.
4. **Run the discriminating check.** Change one relevant variable. A new
   failure or a green rerun without an explanation does not prove the cause.
   For concurrency, coordinate tests through channels or observable state;
   arbitrary sleeps conceal ordering defects.
5. **Fix and preserve the regression.** Assert the externally relevant behavior
   on the original code, observe the expected failure, then make the smallest
   coherent correction. Remove temporary diagnostics. Run the focused check
   and tests for the affected contract; expand according to the risk.

After two unsuccessful fix attempts, stop adding patches. Summarize the
observations, rejected hypotheses and ownership assumptions; choose a new
discriminating check or request an independent analysis. Escalate to the user
only when an actual product decision or missing access prevents progress.

An unavailable toolchain, missing envtest binary or exhausted execution budget
is a validation blocker, not evidence that product code is wrong. Report it
without weakening the test or repeatedly attempting the same unavailable step.

This is a kc-specific adaptation of Superpowers' root-cause-first workflow;
see [sources and licenses](../../../../docs/agent-workflow.md#sources-and-licenses).
