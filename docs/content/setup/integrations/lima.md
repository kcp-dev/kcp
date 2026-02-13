# Lima

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

## Initialize the VM

```sh
limactl create --name=kcp ./kcp.yaml
```

## Start the VM

```sh
limactl start kcp --vm-type=qemu
```

!!! info
    On macOS, Lima may default to vz (Apple Virtualization), while on Linux it defaults to qemu, and on Windows to wsl2. If you want consistency across environments, you can explicitly pass --vm-type=qemu when starting the VM.

## Export the KCP kubeconfig

```sh
export KUBECONFIG="/Users/<user>/.lima/kcp/copied-from-guest/kubeconfig.yaml"
```

## Verify API resources

```sh
kubectl api-resources | grep kcp
```

You should see kcp-specific resources such as:

```sh
workspaces             ws   tenancy.kcp.io/v1alpha1   false   Workspace
logicalclusters             core.kcp.io/v1alpha1     false   LogicalCluster
...
```
