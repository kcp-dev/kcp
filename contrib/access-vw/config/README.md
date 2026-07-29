# Deploying the Access Virtual Workspace

The repo ships three layers of manifest. Apply in this order against the kcp instance hosting the access VW.

## 1. System APIExport (apply once, in a system workspace)

```sh
kubectl --context system kcp ws use root           # or wherever access.kcp.io should live
kubectl apply -f config/apiexport/apiresourceschema.yaml
kubectl apply -f config/apiexport/apiexport.yaml
```

This creates the `SelfClusterAccessReview` schema and the `access.kcp.io` APIExport. kcp will generate an `APIExportEndpointSlice` named after the export — that's what `access-vw --apiexport-endpointslice` points at.

Grab the system identity kubeconfig for the workspace where the APIExport lives; the controller authenticates as that identity to reach the APIExport's virtual workspace and watch bound resources across all consumer workspaces. Save it as a secret in the controller's namespace:

```sh
kubectl create secret generic access-vw-kubeconfig --from-file=kubeconfig=./access-vw.kubeconfig
```

## 2. Controller deployment (apply once)

```sh
kubectl apply -f config/deployment/deployment.yaml
```

The deployment runs the `cmd/access-vw` binary in multi-shard mode. It does NOT need to be deployed in a kcp workspace — the controller is a normal Kubernetes deployment that talks to kcp via the kubeconfig in the secret. It can live anywhere reachable from kcp (its sidecar host, a management cluster, etc.).

## 3. Consumer opt-in (per workspace that wants discovery)

```sh
kubectl --context user kcp ws use my-workspace
kubectl apply -f config/examples/apibinding-consumer.yaml
```

Until a workspace applies this APIBinding **and** accepts the permission claims, it stays invisible to the indexer. That's the opt-in mechanism the design calls for.

## 4. FrontProxy routing (deployment-specific)

The SCAR endpoint path is `/services/access/apis/access.kcp.io/v1alpha1/selfclusteraccessreviews`. FrontProxy needs to forward the `/services/access` prefix to the controller's Service, using a backend of `https://<service>:9443` with `backend_server_ca` plus `proxy_client_cert` / `proxy_client_key` (the requestheader client certificate). The exact mechanism is deployment-dependent:

- kcp-operator deployments configure backends through the operator's CR.
- Bare kcp deployments configure FrontProxy via its config file (`pathMappings:` section in some versions).

In all cases, FrontProxy is responsible for authenticating the caller (bearer / cert) and injecting `X-Remote-User` / `X-Remote-Group` before forwarding. The controller trusts those headers only when FrontProxy presents its requestheader client certificate (verified against `--requestheader-client-ca-file`), the same pattern as the Kubernetes API server aggregation layer.

## Verifying

Once all four layers are in place:

```sh
curl -k -X POST \
  -H "Authorization: Bearer $KCP_TOKEN" \
  -H "Content-Type: application/json" -d '{}' \
  https://kcp.example.com/services/access/apis/access.kcp.io/v1alpha1/selfclusteraccessreviews
```

Expected response shape:

```json
{
  "kind": "SelfClusterAccessReview",
  "apiVersion": "access.kcp.io/v1alpha1",
  "status": {
    "clusters": [
      {"clusterName": "abc123", "endpoint": "https://kcp.example.com/clusters/abc123"}
    ]
  }
}
```
