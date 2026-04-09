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

# This script deploys kcp for loadtesting. Please refer to the loadtesting
# documentation to see how your Kubernetes cluster and its nodePools should be configured.

set -o errexit
set -o nounset
set -o pipefail

cd "$(dirname "$0")"

# optional variables
KCP_OPERATOR_VERSION="${KCP_OPERATOR_VERSION:-0.6.0}"
ETCD_DRUID_VERSION="${ETCD_DRUID_VERSION:-v0.35.0}"
CERT_MANAGER_VERSION="${CERT_MANAGER_VERSION:-v1.19.3}"
ENVOY_GATEWAY_VERSION="${ENVOY_GATEWAY_VERSION:-v1.7.0}"
KUBE_PROMETHEUS_STACK_VERSION="${KUBE_PROMETHEUS_STACK_VERSION:-83.3.0}"
LOKI_VERSION="${LOKI_VERSION:-6.55.0}"
PROMTAIL_VERSION="${PROMTAIL_VERSION:-6.17.1}"
PYROSCOPE_VERSION="${PYROSCOPE_VERSION:-1.20.3}"
USE_DEFAULT_ENVOY_GATEWAY="${USE_DEFAULT_ENVOY_GATEWAY:-true}"
USE_PYROSCOPE="${USE_PYROSCOPE:-false}"

# required variables
: "${NODEPOOL_SELECTOR:? must be set to indicate which label is used for pooling nodes (e.g. worker.gardener.cloud/pool). This depends on your infrastructure setup. Please refer to the architecture document to see which pools should exist}"
: "${GATEWAY_BASE_URL:? must be set to indicate the base domain for the gateway. Needed to setup kcp correctly.}"

if [ "$USE_DEFAULT_ENVOY_GATEWAY" = "true" ]; then
echo "Running with default envoy gateway. If you want to setup ingress/gateway manually, set USE_DEFAULT_ENVOY_GATEWAY to false"
: "${GATEWAY_ANNOTATIONS:? annotations to set on the gateway. Format: GATEWAY_ANNOTATIONS=\'{\"key1\":\"value1\"\}, {\"key2\":\"value2\"\}\'}"
fi

if [ "$USE_PYROSCOPE" = "true" ]; then
  echo "Pyroscope will be deployed for continuous profiling. This can adversely impact performance. Only use it for debugging. Set USE_PYROSCOPE to false to disable it."
fi

echo "Configuring helm repositories"
helm repo add kcp https://kcp-dev.github.io/helm-charts
helm repo add jetstack https://charts.jetstack.io
helm repo add grafana https://grafana.github.io/helm-charts
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm repo update

echo "Installing cert-manager"
helm upgrade --install cert-manager jetstack/cert-manager \
  --version "${CERT_MANAGER_VERSION}" \
  --namespace cert-manager --create-namespace \
  --values=<(envsubst < values/cert-manager.yaml)
echo "Installing selfsigned issuer"
kubectl get namespace kcp || kubectl create namespace kcp
kubectl apply -f <(envsubst < manifests/cert-manager.yaml)

echo "waiting for cert-manager to become ready"
kubectl wait --for=condition=available deployment/cert-manager --namespace cert-manager --timeout=60s

echo "Installing etcd-druid"
helm upgrade --install etcd-druid oci://europe-docker.pkg.dev/gardener-project/releases/charts/gardener/etcd-druid \
  --version "${ETCD_DRUID_VERSION}" \
  --namespace etcd --create-namespace \
  --values=<(envsubst < values/etcd-druid.yaml)

echo "Installing kcp-operator"
helm upgrade --install kcp-operator kcp/kcp-operator \
  --version "${KCP_OPERATOR_VERSION}" \
  --namespace kcp --create-namespace \
  --values=<(envsubst < values/kcp-operator.yaml)

echo "Waiting for etcd-druid to become ready"
kubectl wait --for=condition=available deployment/etcd-druid --namespace etcd --timeout=60s

echo "Waiting for kcp-operator to become ready"
kubectl wait --for=condition=available deployment/kcp-operator --namespace kcp --timeout=60s

echo "Creating etcd clusters"
kubectl get namespace etcd-system || kubectl create namespace etcd-system
kubectl apply -f <(envsubst < manifests/etcd.yaml)

echo "Waiting for etcd clusters to become ready"
kubectl wait --for=condition=Ready etcds/kcp-etcd-shard-1 --namespace etcd-system --timeout=200s
kubectl wait --for=condition=Ready etcds/kcp-etcd-shard-2 --namespace etcd-system --timeout=200s
kubectl wait --for=condition=Ready etcds/kcp-etcd-shard-3 --namespace etcd-system --timeout=200s

echo "Creating kcp shards"
kubectl apply -f <(envsubst < manifests/kcp.yaml)

echo "Waiting for kcp shards to become ready"
kubectl wait --for=jsonpath='{.status.phase}'=Running rootshard/root -n kcp --timeout=300s
kubectl wait --for=jsonpath='{.status.phase}'=Running shard/shard2 -n kcp --timeout=300s
kubectl wait --for=jsonpath='{.status.phase}'=Running shard/shard3 -n kcp --timeout=300s

echo "Generating admin kubeconfig and saving it to admin.kubeconfig"
kubectl apply -f manifests/kcp-admin-kubeconfig-req.yaml
kubectl wait -n kcp --for=create secret/kubeconfig-kcp-admin --timeout=120s
kubectl get secret -n kcp kubeconfig-kcp-admin -o jsonpath="{.data.kubeconfig}" | base64 -d > admin.kubeconfig


echo "Requesting metrics-viewer kubeconfig"
kubectl apply -f manifests/metrics-viewer-kubeconfig-req.yaml
kubectl wait -n kcp --for=create secret/kubeconfig-metrics-viewer --timeout=120s

echo "Copying metrics-viewer client cert to monitoring namespace"
kubectl get namespace monitoring || kubectl create namespace monitoring
# The kcp-operator stores a full kubeconfig in the secret. We need to extract
# client-certificate-data and client-key-data into a TLS secret for the ServiceMonitor.
METRICS_KUBECONFIG=$(kubectl get secret kubeconfig-metrics-viewer -n kcp -o jsonpath='{.data.kubeconfig}' | base64 -d)
METRICS_CRT=$(echo "$METRICS_KUBECONFIG" | yq -r '.users[0].user["client-certificate-data"]')
METRICS_KEY=$(echo "$METRICS_KUBECONFIG" | yq -r '.users[0].user["client-key-data"]')
kubectl create secret tls kcp-metrics-client-cert -n monitoring \
  --cert=<(echo "$METRICS_CRT" | base64 -d) \
  --key=<(echo "$METRICS_KEY" | base64 -d) \
  --dry-run=client -o yaml | kubectl apply -f -
unset METRICS_KUBECONFIG METRICS_CRT METRICS_KEY

echo "Installing kube-prometheus-stack"
helm upgrade --install kube-prometheus-stack prometheus-community/kube-prometheus-stack \
  --version "${KUBE_PROMETHEUS_STACK_VERSION}" \
  --namespace monitoring --create-namespace \
  --values=<(envsubst < values/kube-prometheus-stack.yaml)

if [ "$USE_PYROSCOPE" = "true" ]; then
  echo "Installing pyroscope"
  helm upgrade --install pyroscope grafana/pyroscope \
    --version "${PYROSCOPE_VERSION}" \
    --namespace monitoring --create-namespace \
    --values=<(envsubst < values/pyroscope.yaml)
fi

echo "Installing loki"
helm upgrade --install loki grafana/loki \
  --version "${LOKI_VERSION}" \
  --namespace monitoring --create-namespace \
  --values=<(envsubst < values/loki.yaml)

echo "Installing promtail"
helm upgrade --install promtail grafana/promtail \
  --version "${PROMTAIL_VERSION}" \
  --namespace monitoring --create-namespace \
  --values values/promtail.yaml

echo "Creating kcp ServiceMonitors"
kubectl apply -f manifests/monitoring.yaml

echo "Applying Grafana dashboards"
for dashboard_file in manifests/dashboards/*.json; do
  dashboard_name="$(basename "$dashboard_file" .json | tr '_' '-')"
  kubectl create configmap "grafana-dashboard-${dashboard_name}" \
    --namespace monitoring \
    --from-file="$(basename "$dashboard_file")=${dashboard_file}" \
    --dry-run=client -o yaml \
  | kubectl label --local -f - grafana_dashboard=1 -o yaml --dry-run=client \
  | kubectl annotate --local -f - grafana_folder="kcp loadtests" -o yaml --dry-run=client \
  | kubectl apply -f -
done

echo "Waiting for monitoring stack to become ready"
kubectl wait --for=condition=available deployment/kube-prometheus-stack-grafana --namespace monitoring --timeout=120s
kubectl wait --for=condition=available deployment/kube-prometheus-stack-kube-state-metrics --namespace monitoring --timeout=120s
kubectl wait --for=condition=available deployment/kube-prometheus-stack-operator --namespace monitoring --timeout=120s
kubectl rollout status daemonset/kube-prometheus-stack-prometheus-node-exporter --namespace monitoring --timeout=120s
kubectl rollout status daemonset/promtail --namespace monitoring --timeout=120s
if [ "$USE_PYROSCOPE" = "true" ]; then
  kubectl rollout status statefulset/pyroscope --namespace monitoring --timeout=120s
  kubectl rollout status statefulset/pyroscope-alloy --namespace monitoring --timeout=120s
fi

if [ "$USE_DEFAULT_ENVOY_GATEWAY" = "false" ]; then
  echo "
  All components have been deployed. Now it is time for you to expose them using an ingress/gateway.
  To help you with this, you can check out manifests/gateway.yaml for an example.
  
  Please also create the metrics-viewer RBAC, once you can route to kcp's API server:
  kubectl --kubeconfig admin.kubeconfig apply -f manifests/monitoring-kcp-rbac.yaml
  "
  exit 0
fi

echo "Setting up default envoy gateway"
helm upgrade --install envoy-gateway oci://docker.io/envoyproxy/gateway-helm \
  --version "${ENVOY_GATEWAY_VERSION}" \
  --namespace envoy-gateway-system --create-namespace

echo "Waiting for envoy gateway to become ready"
kubectl wait --for=condition=available deployment/envoy-gateway --namespace envoy-gateway-system --timeout=60s

echo "Configuring gateway for kcp"
# this is just fancy yq for a recursive envsubst operator
yq '(.. | select(tag == "!!str")) |= (envsubst | from_yaml)' manifests/gateway.yaml -P | kubectl apply -f -

echo "Waiting for kcp-apiserver to be reachable"
timeout=$(($(date +%s) + 300))
last_err=""
first_try=true
until last_err=$(kubectl --kubeconfig admin.kubeconfig get shards 2>&1); do
  if [ "$first_try" = true ]; then
    first_try=false
    echo "kcp-apiserver not reachable yet:"
    echo "$last_err"
    echo "Retrying..."
  fi
  if [ "$(date +%s)" -ge "$timeout" ]; then
    echo "Timed out waiting for kcp-apiserver to be reachable."
    echo "Last error: $last_err"
    exit 1
  fi
  sleep 5
done

echo "Applying metrics-viewer RBAC in kcp root workspace"
kubectl --kubeconfig admin.kubeconfig apply -f manifests/monitoring-kcp-rbac.yaml

echo "
-------------------------
Installation has finished successfully. You can now use the generated admin.kubeconfig to access kcp.
You should see 3 shards (incl. rootshard).

export KUBECONFIG=$(pwd)/admin.kubeconfig
kubectl get shards

----

To access Grafana: user: admin, for pw run
kubectl get secret -n monitoring kube-prometheus-stack-grafana -o jsonpath='{.data.admin-password}' | base64 -d
kubectl port-forward -n monitoring svc/kube-prometheus-stack-grafana 3000:80
"
