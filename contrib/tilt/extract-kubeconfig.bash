#!/usr/bin/env bash

set -xeo pipefail

while [[ "$(kubectl api-resources --api-group operator.kcp.io | wc -l)" -lt 2 ]] ; do
    echo "Waiting for kcp-operator API to be available..."
    sleep 1
done

_extract() {
    local kubeconfig="$1"

    kubectl wait "kubeconfig/$kubeconfig" --for=create --timeout=5m
    kubectl wait "kubeconfig/$kubeconfig" --for=condition=Available --timeout=5m
    kubectl wait "secret/kcp-$kubeconfig-kubeconfig" --for=create --timeout=5m
    kubectl get "secret/kcp-$kubeconfig-kubeconfig" -o jsonpath='{.data.kubeconfig}' \
        | base64 -d > "../../tilt-$kubeconfig.kubeconfig"
}

_extract frontproxy
_extract root
_extract theseus
