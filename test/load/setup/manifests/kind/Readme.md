# Kind Example

This serves as an example for the appropriate values which should be used
for node labels.
You can replace the 'role' key, with a value your infra providers
uses for grouping nodes.
Outside of infrastructure development, the full-scale loadtests are not intended to be run on
a kind cluster.

## How To

1. Create kind cluster

    ```sh
    kind create cluster --config manifests/kind/cluster.yaml
    ```

1. Create service aliases

    ```sh
    kubectl apply -f manifests/kind/service-aliases.yaml
    ```

1. Source env values

    ```sh
    export NODEPOOL_SELECTOR="role"
    export GATEWAY_BASE_URL="kcp.svc.cluster.local"
    export GATEWAY_ANNOTATIONS="{'hello':'world'}"
    ```

1. Run setup.sh

    ```sh
    ./setup.sh
    ```

1. Now open a shell on the target cluster (note this is only necessary on the kind setup, for any other you can directly access kcp)

    ```sh
    kubectl run kubectl-shell -n kcp --rm -it \
        --image=bitnami/kubectl \
        --overrides='
    {
      "spec": {
        "containers": [
          {
            "name": "kubectl-shell",
            "image": "bitnami/kubectl",
            "command": ["bash"],
            "stdin": true,
            "tty": true,
            "env": [
              {
                "name": "KUBECONFIG",
                "value": "/root/.kube/kubeconfig"
              }
            ],
            "volumeMounts": [
              {
                "name": "kubeconfig-vol",
                "mountPath": "/root/.kube",
                "readOnly": true
              }
            ]
          }
        ],
        "volumes": [
          {
            "name": "kubeconfig-vol",
            "secret": {
              "secretName": "kubeconfig-kcp-admin"
            }
          }
        ]
      }
    }'
    ```

1. Inside you can talk with the frontproxy

    ```sh
    kubectl get shards --server https://frontproxy-front-proxy.kcp.svc.cluster.local:6443/clusters/root
    ```

If you want to talk to other components, you will need to generate a kubeconfig using the operator. Refer to kcp-admin-kubeconfig-req.yaml for an example.
