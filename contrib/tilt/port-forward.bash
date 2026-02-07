#!/usr/bin/env bash

set -xeo pipefail

while [[ "$(kubectl api-resources --api-group gateway.networking.k8s.io | wc -l)" -lt 2 ]] ; do
    echo "Waiting for Gateway API to be available..."
    sleep 1
done

kubectl -n envoy-gateway-system wait gateway/eg --for=create --timeout=5m
kubectl -n envoy-gateway-system wait gateway/eg --for=condition=Programmed --timeout=5m

svc_name="$(kubectl -n envoy-gateway-system \
    get svc -l gateway.envoyproxy.io/owning-gateway-name=eg \
    -o jsonpath='{.items[0].metadata.name}'
)"

kubectl -n envoy-gateway-system port-forward "svc/$svc_name" 8443:8443
