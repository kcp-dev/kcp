#!/usr/bin/env bash

set -euo pipefail

hostname="$(yq '.externalHostname' < contrib/tilt/kcp-values.yaml)"

cat << EOF > kcp.kubeconfig
apiVersion: v1
kind: Config
clusters:
  - cluster:
      insecure-skip-tls-verify: true
      server: "https://$hostname/clusters/root"
    name: kind-kcp
contexts:
  - context:
      cluster: kind-kcp
      user: kind-kcp
    name: kind-kcp
current-context: kind-kcp
users:
  - name: kind-kcp
    user:
      token: admin-token
EOF

echo "Kubeconfig file created at kcp.kubeconfig"
echo ""
echo "export KUBECONFIG=kcp.kubeconfig"
echo ""
