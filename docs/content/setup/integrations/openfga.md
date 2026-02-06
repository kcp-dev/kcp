# OpenFGA

kcp can integrate with [OpenFGA](https://openfga.dev/) via a shim webhook component that accepts kcp's [authorization webhooks](../../concepts/authorization/authorizers.md#webhook-authorizer) and translates
them to OpenFGA queries.

!!! info "Third Party Solutions"
    A third-party example of such a webhook would be Platform Mesh's [rebac-authz-webhook](https://github.com/platform-mesh/rebac-authz-webhook).
