#!/usr/bin/env bash

set -euxo pipefail

DEMO_ROOT="$(dirname "${BASH_SOURCE}")/../.."

clusters="$@"
if [[ $# -eq 0 ]]; then
  clusters="us-east1 us-west1"
fi

for name in ${clusters}; do
  if [[ ! -f ${DEMO_ROOT}/clusters/kind/${name}.config ]]; then
    cat > ${DEMO_ROOT}/clusters/kind/${name}.config << EOF
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
name: ${name}
networking:
  apiServerAddress: "127.0.0.1"
EOF
  fi

  kind delete cluster --name=${name} || true
  kind create cluster --config ${DEMO_ROOT}/clusters/kind/${name}.config --kubeconfig ${DEMO_ROOT}/clusters/kind/${name}.kubeconfig

  cat > ${DEMO_ROOT}/clusters/${name}.yaml << EOF
apiVersion: cluster.example.dev/v1alpha1
kind: Cluster
metadata:
  name: ${name}
spec:
  kubeconfig: |
EOF
  sed -e 's/^/    /' ${DEMO_ROOT}/clusters/kind/${name}.kubeconfig >> ${DEMO_ROOT}/clusters/${name}.yaml
done
