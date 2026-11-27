# Command line

Start kc with `kc`. It opens in the current context of your kubeconfig (see [Which context kc opens](#which-context-kc-opens)) and never changes the kubeconfig.

## Kubectl-style shortcuts

Kubernetes Commander accepts a subset of kubectl syntax so you can jump straight to the desired view:

- `kc get pods` – focuses the Pods list for the current/flagged namespace.
- `kc get deploy myapp -o yaml` – highlights `myapp`, opens the manifest viewer in the opposite panel.
- `kc get pods/nginx pods/api svc/frontend` – multi-selects rows across resources.
- `kc logs payments-0 -c worker --follow` – drills down to the pod → container → logs row and opens the streaming viewer.
- `kc overview`, `kc -n shop overview`, `kc -A overview` – start in the [cluster overview](features.md#cluster-overview) of the context's namespace, of `shop`, or of all namespaces.
- `kc dashboard`, `kc -n shop dashboard`, `kc -A dashboard` – start in the full-screen [dashboard](../internal/help/topics/dashboard.md) of the namespace or of all namespaces.

All commands honor `-n/--namespace`; when omitted, kc uses the kubeconfig’s default.

Startup scope flags work as in kubectl, before or after the subcommand:

- `kc --context staging` – start in the `staging` context instead of the current one. kc reads the context from the kubeconfig files it loads (`--kubeconfig`, `$KUBECONFIG` or `~/.kube`; the files of `$KUBECONFIG` are merged as kubectl merges them); an unknown name stops kc before the UI opens, with an error that lists the contexts. kubeconfig is not changed.
- `kc -A`, `kc --context staging -A get pods` – start both panels in the all-namespaces scope, with the Namespace column. `-A` wins over `-n`, as in kubectl.
- `kc get -A` takes types only: `kc get pods -A web` fails, like kubectl, because a name is only unique in one namespace. `kc logs` does not take `-A`.
- `--as`, `--as-group` and `--as-uid` keep working with all of these.

## Which context kc opens

kc reads the kubeconfig files of `--kubeconfig` or `$KUBECONFIG`, else `~/.kube/config` and then the other files under `~/.kube` in name order. Without `--context`, it opens the `current-context` of the first of these files that sets one: for the files of `$KUBECONFIG` that is the current-context kubectl reads from the merged list, and a file under `~/.kube` stands alone, so its current-context names one of its own contexts. kc never opens a context that neither you nor the kubeconfig named:

- A `current-context` that names a context the kubeconfig does not define stops kc before the UI opens, as kubectl refuses it. The error names the context, the file that sets it and the contexts there are, sorted:

  ```
  Error: current-context "typo-context" in /home/me/.kube/config is not defined; contexts: badtoken, good, limited, refused; pick one with --context
  ```

  A later file's current-context does not replace it.
- When no file sets a current-context and there are several contexts, kc stops the same way (`no current-context is set in …`). With exactly one context, kc opens it and says so in a message at the bottom of the screen.
- An unknown `--context` stops kc with the list of contexts.

These errors go to stderr, and kc exits with status 1. Contexts are sorted by name wherever kc lists them, so the context picker shows them in the same order on every run.
