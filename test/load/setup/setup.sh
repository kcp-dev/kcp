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
KCP_OPERATOR_VERSION="${KCP_OPERATOR_VERSION:-0.4.0}"
ETCD_DRUID_VERSION="${ETCD_DRUID_VERSION:-v0.35.0}"
CERT_MANAGER_VERSION="${CERT_MANAGER_VERSION:-v1.19.3}"
ENVOY_GATEWAY_VERSION="${ENVOY_GATEWAY_VERSION:-v1.7.0}"
USE_DEFAULT_ENVOY_GATEWAY="${USE_DEFAULT_ENVOY_GATEWAY:-true}"

# required variables
: "${NODEPOOL_SELECTOR:? must be set to indicate which label is used for pooling nodes (e.g. worker.gardener.cloud/pool). This depends on your infrastructure setup. Please refer to the architecture document to see which pools should exist}"

if [ "$USE_DEFAULT_ENVOY_GATEWAY" = "true" ]; then
echo "Running with default envoy gateway. If you want to setup ingress/gateway manually, set USE_DEFAULT_ENVOY_GATEWAY to false"
: "${GATEWAY_BASE_URL:? must be set to indicate the base domain for the gateway}"
: "${GATEWAY_ANNOTATIONS:? annotations to set on the gateway. Format: GATEWAY_ANNOTATIONS=\'{\"key1\":\"value1\"\}, {\"key2\":\"value2\"\}\'}"
fi

echo "Configuring helm repositories"
helm repo add kcp https://kcp-dev.github.io/helm-charts
helm repo add jetstack https://charts.jetstack.io
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
kubectl wait -n kcp --for=create secret/admin-kubeconfig --timeout=120s
kubectl get secret admin-kubeconfig -o jsonpath="{.data.kubeconfig}" | base64 -d > admin.kubeconfig

if [ "$USE_DEFAULT_ENVOY_GATEWAY" = "false" ]; then
  echo "
  All components have been deployed. Now it is time for you to expose them using an ingress/gateway.
  To help you with this, you can check out manifests/gateway.yaml for an example.
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

echo "
-------------------------
Installation has finished successfully. You can now use the generated admin.kubeconfig to access kcp.
You should see 3 shards (incl. rootshard).

export KUBECONFIG=$(pwd)/admin.kubeconfig
kubectl get shards
"
