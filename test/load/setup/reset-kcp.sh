#!/usr/bin/env bash

# Copyright 2026 The kcp Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# This script resets kcp and all data stored in etcd. It deletes the kcp shards,
# front-proxy, and etcd clusters (including their PVCs), then re-creates them
# from the manifests. Useful between load test runs to start with a clean state.

set -o errexit
set -o nounset
set -o pipefail

cd "$(dirname "$0")"

# required variables
: "${NODEPOOL_SELECTOR:? must be set to indicate which label is used for pooling nodes (e.g. worker.gardener.cloud/pool). This depends on your infrastructure setup. Please refer to the architecture document to see which pools should exist}"
: "${GATEWAY_BASE_URL:? must be set to indicate the base domain for the gateway. Needed to setup kcp correctly.}"

# Must match the value used by setup.sh so we know whether an external
# cache-server needs to be torn down and re-created alongside the shards.
USE_EXTERNAL_CACHE="${USE_EXTERNAL_CACHE:-true}"

echo "=== Resetting kcp and etcd data ==="

echo "Deleting kcp shards and front-proxy and external cache-server (if exists)"
kubectl delete shard --all -n kcp --ignore-not-found
kubectl delete frontproxy --all -n kcp --ignore-not-found
kubectl delete rootshard --all -n kcp --ignore-not-found
kubectl delete cacheserver --all -n kcp --ignore-not-found


echo "Waiting for kcp pods to terminate"
kubectl wait --for=delete pod -l app.kubernetes.io/managed-by=kcp-operator -n kcp --timeout=120s 2>/dev/null || true

echo "Deleting kcp secrets (certificates, kubeconfigs)"
kubectl delete secret -n kcp --all --ignore-not-found

echo "Deleting etcd clusters"
kubectl delete etcd --all -n etcd-system --ignore-not-found

echo "Waiting for etcd pods to terminate"
kubectl wait --for=delete pod -l app=etcd -n etcd-system --timeout=120s 2>/dev/null || true

echo "Deleting etcd PVCs to wipe all stored data"
kubectl delete pvc --all -n etcd-system --ignore-not-found

echo "Waiting for PVCs to be deleted"
kubectl wait --for=delete pvc --all -n etcd-system --timeout=120s 2>/dev/null || true

echo "=== Re-creating etcd clusters ==="
kubectl apply -f <(envsubst < manifests/etcd.yaml)

echo "Waiting for etcd clusters to become ready"
kubectl wait --for=condition=Ready etcds/kcp-etcd-shard-1 --namespace etcd-system --timeout=200s
kubectl wait --for=condition=Ready etcds/kcp-etcd-shard-2 --namespace etcd-system --timeout=200s
kubectl wait --for=condition=Ready etcds/kcp-etcd-shard-3 --namespace etcd-system --timeout=200s

if [ "$USE_EXTERNAL_CACHE" = "true" ]; then
  echo "=== Re-creating external cache-server ==="
  kubectl apply -f <(envsubst < manifests/cache-server.yaml)

  echo "Waiting for cache-server deployment to become ready"
  kubectl wait --for=create deployment/cache-cache-server -n kcp --timeout=120s
  kubectl wait --for=condition=available deployment/cache-cache-server -n kcp --timeout=200s
fi

echo "=== Re-creating kcp shards ==="
if [ "$USE_EXTERNAL_CACHE" = "true" ]; then
  envsubst < manifests/kcp.yaml \
    | yq '(select(.kind == "RootShard" or .kind == "Shard") | .spec.cache) = {"ref": {"name": "cache"}}' \
    | kubectl apply -f -
else
  kubectl apply -f <(envsubst < manifests/kcp.yaml)
fi

echo "Waiting for kcp shards to become ready"
kubectl wait --for=jsonpath='{.status.phase}'=Running rootshard/root -n kcp --timeout=300s
kubectl wait --for=jsonpath='{.status.phase}'=Running shard/shard2 -n kcp --timeout=300s
kubectl wait --for=jsonpath='{.status.phase}'=Running shard/shard3 -n kcp --timeout=300s

echo "Generating admin kubeconfig and saving it to admin.kubeconfig"
kubectl apply -f manifests/kcp-admin-kubeconfig-req.yaml
kubectl wait -n kcp --for=create secret/kubeconfig-kcp-admin --timeout=120s
kubectl get secret -n kcp kubeconfig-kcp-admin -o jsonpath="{.data.kubeconfig}" | base64 -d > admin.kubeconfig

echo "Re-creating metrics-viewer kubeconfig for prometheus scraping"
kubectl apply -f manifests/metrics-viewer-kubeconfig-req.yaml
kubectl wait -n kcp --for=create secret/kubeconfig-metrics-viewer --timeout=120s

echo "Copying metrics-viewer client cert to monitoring namespace"
METRICS_KUBECONFIG=$(kubectl get secret kubeconfig-metrics-viewer -n kcp -o jsonpath='{.data.kubeconfig}' | base64 -d)
METRICS_CRT=$(echo "$METRICS_KUBECONFIG" | yq -r '.users[0].user["client-certificate-data"]')
METRICS_KEY=$(echo "$METRICS_KUBECONFIG" | yq -r '.users[0].user["client-key-data"]')
kubectl create secret tls kcp-metrics-client-cert -n monitoring \
  --cert=<(echo "$METRICS_CRT" | base64 -d) \
  --key=<(echo "$METRICS_KEY" | base64 -d) \
  --dry-run=client -o yaml | kubectl apply -f -
unset METRICS_KUBECONFIG METRICS_CRT METRICS_KEY

echo "Restarting prometheus to pick up new client certificate"
kubectl delete pod -n monitoring -l app.kubernetes.io/name=prometheus --ignore-not-found

echo "=== kcp reset complete ==="
