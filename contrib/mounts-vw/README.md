# Remote mounts virtual workspace example

To run kcp:

```
go run ./cmd/kcp start --mapping-file=./contrib/assets/mounts-vw/path-mapping.yaml --feature-gates=WorkspaceMounts=true
```

Run Virtual workspace:
```
go run ./cmd/virtual-workspaces/ start \
--kubeconfig=../../.kcp/admin.kubeconfig \
--tls-cert-file=../../.kcp/apiserver.crt \
--tls-private-key-file=../../.kcp/apiserver.key \
--authentication-kubeconfig=../../.kcp/admin.kubeconfig
```

Virtual workspace needs to get traffic from mounts acts from index to return url ant matches to mappings:

```
url, errorCode := h.resolveURL(r)
		if errorCode != 0 {
			http.Error(w, http.StatusText(errorCode), errorCode)
			return
		}
		if strings.HasPrefix(url, m.Path) {
			m.Handler.ServeHTTP(w, r)
			return
		}
```

To insert this url into proxy for mounts is annotations.

```
kubectl create -f assets/customresourcedefinition_kubecluster.yaml
kubectl create -f assets/kube-cluster.yaml
```

Patch the KubeCluster with url and fake condition:

```
kubectl patch kubecluster proxy-cluster --type='merge' -p '{
  "status": {
    "phase": "Ready",
    "URL": "http://0.0.0.0:6443/services/cluster-proxy/foo",
    "conditions": [
      {
        "type": "WorkspaceKubeClusterReady",
        "status": "True",
        "lastTransitionTime": "2024-10-22T14:32:50Z"
      }
    ]
  }
}' --subresource='status'
```

Patch workspace with mount annotations:

```
kubectl annotate workspace test \
  experimental.tenancy.kcp.io/mount='{"spec":{"ref":{"kind":"KubeCluster","name":"proxy-cluster","apiVersion":"contrib.kcp.io/v1alpha1"}},"status":{"phase":"Ready"}}'

```
