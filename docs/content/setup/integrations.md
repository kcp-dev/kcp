# Integrations

kcp integrates with several CNCF projects. This page documents known integrations. Please be aware that we try our best to keep it updated but rely on community contributions for that.

kcp has some "obvious" integrations e.g. with [Kubernetes](https://kubernetes.io) (since it can be deployed on a Kubernetes cluster) and [Helm](https://helm.sh) (since a Helm chart is maintained as the
primary installation method on Kubernetes).

The fact that kcp is compatible with the Kubernetes Resource Model (KRM) also means that projects using the Kubernetes API might be compatible. The [api-syncagent](https://docs.kcp.io/api-syncagent)
component also allows integration of *any* Kubernetes controller/operator in principle. An example of this can be found in our [KubeCon London workshop](https://docs.kcp.io/contrib/learning/20250401-kubecon-london/workshop/).

## multicluster-runtime

kcp integrates with [kubernetes-sigs/multicluster-runtime](https://github.com/kubernetes-sigs/multicluster-runtime) by providing a so-called provider which gives a controller dynamic
access to kcp workspaces. Multiple providers exists for different use cases, see [kcp-dev/multicluster-provider](https://github.com/kcp-dev/multicluster-provider) for a full overview.

## Dex

kcp integrates with any OIDC provider, which includes [Dex](https://dexidp.io). To use `kubectl` with it, [kubelogin](https://github.com/int128/kubelogin) is required.

To integrate them make sure to set up a static client in Dex that is configured similar to:

```yaml
staticClients:
- id: kcp-kubelogin
  name: kcp-kubelogin
  secret: <RANDOM-SECRET-HERE>
  RedirectURIs:
  - http://localhost:8000
  - http://localhost:18000
```

Which is then used by [kubelogin](https://github.com/int128/kubelogin) (warning: the secret is shared across all users!). Check its documentation for more details.

A kubeconfig's `users` configuration would look similar to this:

```yaml
users:
- name: oidc
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1beta1
      args:
      - oidc-login
      - get-token
      - --oidc-issuer-url=https://<url-to-dex>
      - --oidc-client-id=kcp-kubelogin
      - --oidc-client-secret=<RANDOM-SECRET-HERE>
      - --oidc-extra-scope=email,groups
      command: kubectl
      env: null
      interactiveMode: IfAvailable
      provideClusterInfo: false
```

## OpenFGA

kcp can integrate with [OpenFGA](https://openfga.dev/) via a shim webhook component that accepts kcp's [authorization webhooks](../concepts/authorization/authorizers.md#webhook-authorizer) and translates
them to OpenFGA queries.

!!! info "Third Party Solutions"
    A third-party example of such a webhook would be Platform Mesh's [rebac-authz-webhook](https://github.com/platform-mesh/rebac-authz-webhook).

## Lima
You can run kcp inside a [Lima](https://github.com/lima-vm/lima)-managed VM, which makes it portable across macOS, Linux, and Windows (via WSL2). This setup gives you a disposable kcp control plane that integrates smoothly with your host kubectl.

!!! info "Development Use Only"
    This is essentially a development environment, where one can start a single instance of kcp for testing or limited-scope use cases. This is in no way intended for production usage.

Create a Lima template for kcp and save the following as `kcp.yaml`:
 ```yaml
minimumLimaVersion: 1.1.0

base: template://_images/ubuntu-lts

mounts: []

containerd:
  system: false
  user: false

provision:
- mode: system
  script: |
    #!/bin/bash
    set -eux -o pipefail
    command -v kcp >/dev/null 2>&1 && exit 0

    export DEBIAN_FRONTEND=noninteractive
    apt-get update
    apt-get install -y curl wget

    KCP_VERSION=$(curl -s https://api.github.com/repos/kcp-dev/kcp/releases/latest | grep tag_name | cut -d '"' -f 4)
    KCP_VERSION_NO_V=${KCP_VERSION#v}

    wget https://github.com/kcp-dev/kcp/releases/download/${KCP_VERSION}/kcp_${KCP_VERSION_NO_V}_linux_arm64.tar.gz
    tar -xzf kcp_${KCP_VERSION_NO_V}_linux_arm64.tar.gz
    mv bin/kcp /usr/local/bin/
    chmod +x /usr/local/bin/kcp
    rm -f kcp_${KCP_VERSION_NO_V}_linux_arm64.tar.gz

    mkdir -p /var/.kcp/
    sudo chmod 755 /var/.kcp

    cat > /etc/systemd/system/kcp.service << EOF
    [Unit]
    Description=kcp server
    After=network.target

    [Service]
    Type=simple
    User=root
    ExecStart=/usr/local/bin/kcp start --root-directory=/var/.kcp/ --bind-address=127.0.0.1
    Restart=on-failure
    StandardOutput=journal
    StandardError=journal

    [Install]
    WantedBy=multi-user.target
    EOF

    systemctl daemon-reload
    systemctl enable kcp
    systemctl start kcp

probes:
- script: |
    #!/bin/bash
    set -eux -o pipefail
    if ! timeout 120s bash -c "until curl -f -s --cacert /var/.kcp/apiserver.crt https://127.0.0.1:6443/readyz >/dev/null; do sleep 3; done"; then
      echo >&2 "kcp is not ready yet"
      exit 1
    fi
  hint: |
    The kcp control plane is not ready yet.
    Check the kcp logs with "limactl shell kcp sudo journalctl -f" or "tail -f /var/log/kcp.log"

copyToHost:
- guest: "/var/.kcp/admin.kubeconfig"
  host: "{{ '{{.Dir}}' }}/copied-from-guest/kubeconfig.yaml"
  deleteOnStop: true

message: |
  To run `kubectl` on the host (assumes kubectl is installed), run:
  ------
  export KUBECONFIG="{{ '{{.Dir}}' }}/copied-from-guest/kubeconfig.yaml"
  kubectl get workspaces
  ------

 ```
Initialize the VM
```sh
limactl create --name=kcp ./kcp.yaml
```

Start the VM
```sh
limactl start kcp --vm-type=qemu
```
!!! info
On macOS, Lima may default to vz (Apple Virtualization), while on Linux it defaults to qemu, and on Windows to wsl2. If you want consistency across environments, you can explicitly pass --vm-type=qemu when starting the VM.

Export the KCP kubeconfig
```sh
export KUBECONFIG="/Users/<user>/.lima/kcp/copied-from-guest/kubeconfig.yaml"
```

Verify API resources
```sh
kubectl api-resources | grep kcp
```
You should see kcp-specific resources such as:
```sh
workspaces             ws   tenancy.kcp.io/v1alpha1   false   Workspace
logicalclusters             core.kcp.io/v1alpha1     false   LogicalCluster
...
```

