# Global cluster-aware validating admission webhook

kcp admission webhooks are normally registered *per logical cluster* (a
`ValidatingWebhookConfiguration` object lives in, and fires only for, one
workspace) — or, for APIExport-bound resources, once in the provider workspace
for that export's resources. There was no way to register **one** webhook that
fires for **every** workspace, the way `--authorization-webhook-config-file`
wires a single global authorization webhook.

The `apis.kcp.io/GlobalValidatingWebhook` admission plugin fills that gap: a
single `ValidatingWebhookConfiguration`, supplied to the shard at **startup**,
applied to **all** logical clusters. Unlike the authorization webhook it is an
**admission** webhook — the callout carries the full object body + operation and
can **deny**. Because it is wired into the shard (not an in-workspace object), a
workspace admin cannot see or remove it.

## How the webhook learns the cluster

The reviewed object carries the request's logical cluster on its
`kcp.io/cluster` annotation, stamped for the duration of the callout (create/update
on the new object, delete on the old object) and reverted afterwards — so a single
endpoint is cluster-aware on every operation.

## Use

```sh
kcp start \
  --admission-control-config-file=config/examples/global-admission-webhook/admission-config.yaml
```

- `admission-config.yaml` — an `AdmissionConfiguration` pointing the plugin at the
  webhook config.
- `global-validatingwebhookconfiguration.yaml` — a normal
  `ValidatingWebhookConfiguration` (URL clientConfig + caBundle). Serve
  AdmissionReview `v1`.

The plugin is default-on but **inert** unless this config is supplied.

## Notes / guardrails

- It is on the write path of every matching request across every workspace —
  keep `failurePolicy` and `timeoutSeconds` tight, and narrow `rules` /
  `matchConditions` (CEL) so you are only called for objects you care about.
- Only **URL** clientConfig is exercised (service resolution is not wired for the
  static source). Provide the `caBundle`.
- This is the validating (deny) variant; a mutating sibling can be added the same
  way if needed.
