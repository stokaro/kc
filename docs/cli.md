# Command line

Start kc with `kc`. It opens in the current context of your kubeconfig and never changes the kubeconfig.

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

- `kc --context staging` – start in the `staging` context instead of the current one. kc reads the context from the kubeconfig files it loads (`--kubeconfig`, `$KUBECONFIG` or `~/.kube`; the files of `$KUBECONFIG` are merged as kubectl merges them); an unknown name stops kc with an error before the UI opens. kubeconfig is not changed.
- `kc -A`, `kc --context staging -A get pods` – start both panels in the all-namespaces scope, with the Namespace column. `-A` wins over `-n`, as in kubectl.
- `kc get -A` takes types only: `kc get pods -A web` fails, like kubectl, because a name is only unique in one namespace. `kc logs` does not take `-A`.
- `--as`, `--as-group` and `--as-uid` keep working with all of these.
