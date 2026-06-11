#!/usr/bin/env bash

set -xeo pipefail

while [[ "$(kubectl api-resources --api-group gateway.networking.k8s.io | wc -l)" -lt 2 ]] ; do
    echo "Waiting for Gateway API to be available..."
    sleep 1
done

kubectl -n envoy-gateway-system wait gateway/eg --for=create --timeout=5m
kubectl -n envoy-gateway-system wait gateway/eg --for=condition=Programmed --timeout=5m

# Keep the port-forward alive across Envoy pod churn (e.g. a kind/control-plane
# restart recreates the gateway pod with a new UID, which resets the existing
# forward). Without this loop kubectl exits on the first reset and the ingress
# disappears until Tilt re-runs the resource. We re-resolve the service name on
# every iteration because the gateway's backing Service is also regenerated.
while true ; do
    # `|| true` so a transient API error during pod churn does not trip `set -e`
    # and kill the loop.
    svc_name="$(kubectl -n envoy-gateway-system \
        get svc -l gateway.envoyproxy.io/owning-gateway-name=eg \
        -o jsonpath='{.items[0].metadata.name}' || true
    )"

    if [[ -n "$svc_name" ]] ; then
        kubectl -n envoy-gateway-system port-forward "svc/$svc_name" 8443:8443 || true
    fi

    echo "port-forward dropped, reconnecting in 2s..."
    sleep 2
done
