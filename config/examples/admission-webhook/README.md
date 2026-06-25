# Validating objects across an APIExport/APIBinding

This example shows how an **API provider** enforces validation on a custom
resource (`Cowboy`) for **every consumer workspace** that binds the API — by
registering the validation **once**, in the workspace that owns the `APIExport`.

See also: [docs/content/concepts/apis/admission-webhooks.md](../../../docs/content/concepts/apis/admission-webhooks.md).

## The key idea

kcp makes admission **cluster-aware**. When a `Cowboy` is created in a consumer
workspace, kcp's admission plugin notices the resource schema comes from an
`APIBinding`, resolves the owning `APIExport`, and dispatches admission to the
`ValidatingWebhookConfiguration` / `ValidatingAdmissionPolicy` that live **in the
provider workspace** — not the consumer's. The reviewed object is annotated with
`kcp.io/cluster: <consumer>` so the webhook knows which workspace it lives in.

So you do **not** install a webhook in each consumer. You install it once where
the `APIExport` lives, and it covers all bindings.

```
root:provider                                root:consumer
┌──────────────────────────────┐            ┌─────────────────────────┐
│ APIResourceSchema (cowboys)  │            │ APIBinding ─────────────┼─┐
│ APIExport (today-cowboys) ◄──┼────────────┼─────────────────────────┘ │
│ ValidatingWebhookConfig      │            │ Cowboy (intent: bad) ─────┼─┐
│  (or ValidatingAdmissionPol.)│            └───────────────────────────┘ │
└──────────────────────────────┘                                          │
            ▲  admission of the consumer's Cowboy is dispatched here ─────┘
```

## Files

| File | Workspace | What |
|------|-----------|------|
| `apiresourceschema.yaml` | `root:provider` | Schema of the `Cowboy` API |
| `apiexport.yaml`         | `root:provider` | Exports the API |
| `apibinding.yaml`        | `root:consumer` | Binds the export |
| `validatingwebhookconfiguration.yaml` | `root:provider` | Webhook (external server); `url`/`caBundle` templated by `run.sh` |
| `validatingadmissionpolicy.yaml`      | `root:provider` | Server-less CEL alternative |
| `webhook/main.go`                     | host | Stdlib-only server: rejects `spec.intent == "bad"` |
| `cowboy-good.yaml` / `cowboy-bad.yaml`| `root:consumer` | Admitted / rejected |
| `run.sh` | — | One-shot driver for both modes |

## Quick start

Requires a running kcp reachable via `$KUBECONFIG` (defaults to the repo root
`admin.kubeconfig`) and the `kubectl ws` plugin (`make install` or `bin/kubectl-ws`).

```bash
# webhook mode (external server). kcp must be able to reach this host; the script
# uses host.docker.internal, which works for the Tilt/kind setup on Docker Desktop.
./config/examples/admission-webhook/run.sh

# server-less alternative (ValidatingAdmissionPolicy, no certs, no server):
./config/examples/admission-webhook/run.sh policy

# tear down the demo workspaces:
./config/examples/admission-webhook/run.sh clean
```

Expected tail of a successful run:

```
== TEST 1: create a GOOD cowboy (expect ADMITTED)
✓ good cowboy admitted
== TEST 2: create a BAD cowboy (expect REJECTED)
✓ bad cowboy rejected as expected:
    Error from server: admission webhook "validate.cowboys.wildwest.dev" denied
    the request: cowboy "bad-bart" rejected: spec.intent must not be "bad"
    (workspace <consumer-cluster>)
```

The provider webhook server's log shows the reviewed object's
`kcp.io/cluster` annotation = the consumer workspace, proving the
cross-workspace dispatch.

## Manual walkthrough (webhook mode)

```bash
export KUBECONFIG=$PWD/admin.kubeconfig   # from repo root

# 1. Provider: schema + export
kubectl ws :root
kubectl ws create provider --ignore-existing --enter
kubectl apply -f config/examples/admission-webhook/apiresourceschema.yaml
kubectl apply -f config/examples/admission-webhook/apiexport.yaml

# 2. Provider: run the webhook server + register it (see run.sh for cert/caBundle wiring)
#    url: https://host.docker.internal:9443/validate

# 3. Consumer: bind the export
kubectl ws :root
kubectl ws create consumer --ignore-existing --enter
kubectl apply -f config/examples/admission-webhook/apibinding.yaml

# 4. Test from the consumer workspace
kubectl apply -f config/examples/admission-webhook/cowboy-good.yaml   # admitted
kubectl apply -f config/examples/admission-webhook/cowboy-bad.yaml    # denied
```

## Notes

- The webhook server (`webhook/main.go`) uses only the standard library, so it
  has no dependencies to download. Run it with `go run .` from that directory.
- `failurePolicy: Fail` means a Cowboy create is rejected if the webhook server is
  unreachable — start the server before creating objects (run.sh does this).
- For the external-webhook path kcp must be able to dial the server. In the
  Tilt/kind setup, `host.docker.internal` resolves from inside the kind node to
  the host. For other topologies, set a routable `url` and matching cert SAN.
