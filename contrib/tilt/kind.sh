#!/usr/bin/env bash

# Copyright 2021 The KCP Authors.
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

set -e

ARCH=""
case $(uname -m) in
    i386)   ARCH="386" ;;
    i686)   ARCH="386" ;;
    x86_64) ARCH="amd64" ;;
    arm)    dpkg --print-architecture | grep -q "arm64" && ARCH="arm64" || ARCH="arm" ;;
esac

if [ ! -f "/usr/local/bin/kind" ]; then
 echo "Installing KIND"
 curl -Lo ./kind https://kind.sigs.k8s.io/dl/v0.20.0/kind-linux-$ARCH
 chmod +x ./kind
 sudo mv ./kind /usr/local/bin/kind
else
    echo "KIND already installed"
fi

if [ ! -f "$HOME/.local/bin/tilt" ]; then
 echo "Installing TILT"
 curl -fsSL https://raw.githubusercontent.com/tilt-dev/tilt/master/scripts/install.sh | bash
else
    echo "TILT already installed"
fi

CLUSTER_NAME=${CLUSTER_NAME:-kcp}
echo "CLUSTER_NAME: ${CLUSTER_NAME}"

# create registry container unless it already exists
REGISTRY_NAME='kcp-registry'
REGISTRY_PORT='5000'
running="$(docker inspect -f '{{.State.Running}}' "${REGISTRY_NAME}" 2>/dev/null || true)"
if [ "${running}" != 'true' ]; then
  docker run \
    -d --restart=always -p "127.0.0.1:${REGISTRY_PORT}:5000" --name "${REGISTRY_NAME}" \
    registry:2
fi

if ! kind get clusters | grep -w -q "${CLUSTER_NAME}"; then
# create a cluster with the local registry enabled in containerd
cat <<EOF | kind create cluster --config=-
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
name: ${CLUSTER_NAME}
featureGates:
  EphemeralContainers: true
kubeadmConfigPatches:
- |
  apiVersion: kubeadm.k8s.io/v1beta2
  kind: ClusterConfiguration
  metadata:
    name: config
  apiServer:
    extraArgs:
      "enable-admission-plugins": NamespaceLifecycle,LimitRanger,ServiceAccount,TaintNodesByCondition,Priority,DefaultTolerationSeconds,DefaultStorageClass,PersistentVolumeClaimResize,MutatingAdmissionWebhook,ValidatingAdmissionWebhook,ResourceQuota
nodes:
  - role: control-plane
    image: kindest/node:v1.26.6
    kubeadmConfigPatches:
      - |
        kind: InitConfiguration
        nodeRegistration:
          kubeletExtraArgs:
            node-labels: "ingress-ready=true"
    extraPortMappings:
      - containerPort: 443
        hostPort: 6443
        protocol: TCP
      - containerPort: 80
        hostPort: 6440
        protocol: TCP

containerdConfigPatches:
- |-
  [plugins."io.containerd.grpc.v1.cri".registry.mirrors."localhost:${REGISTRY_PORT}"]
    endpoint = ["http://${REGISTRY_NAME}:${REGISTRY_PORT}"]
  [plugins."io.containerd.grpc.v1.cri".containerd]
    discard_unpacked_layers = false
EOF

else
    echo "Cluster already exists"
fi

docker network connect "kind" "${REGISTRY_NAME}" || true

kind export kubeconfig --name "$CLUSTER_NAME"

# Document the local registry
# https://github.com/kubernetes/enhancements/tree/master/keps/sig-cluster-lifecycle/generic/1755-communicating-a-local-registry
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: ConfigMap
metadata:
  name: local-registry-hosting
  namespace: kube-public
data:
  localRegistryHosting.v1: |
    host: "localhost:${REGISTRY_PORT}"
    help: "https://kind.sigs.k8s.io/docs/user/local-registry/"
EOF


# TODO: Move to tilt
echo "Installing ingress"

kubectl apply -f https://raw.githubusercontent.com/kubernetes/ingress-nginx/main/deploy/static/provider/kind/deploy.yaml
kubectl label nodes ${CLUSTER_NAME}-control-plane node-role.kubernetes.io/control-plane-

echo "Waiting for the ingress controller to become ready..."
# https://github.com/kubernetes/kubernetes/issues/83242
until kubectl --context "${KUBECTL_CONTEXT}" -n ingress-nginx get pod -l app.kubernetes.io/component=controller -o go-template='{{.items | len}}' | grep -qxF 1; do
    echo "Waiting for pod"
    sleep 1
done
kubectl --context "${KUBECTL_CONTEXT}" -n ingress-nginx wait --for=condition=Ready pod -l app.kubernetes.io/component=controller --timeout=5m

echo "Installing cert-manager"

helm repo add jetstack https://charts.jetstack.io
helm repo update

kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.9.1/cert-manager.crds.yaml
helm upgrade -i \
  --wait \
  cert-manager jetstack/cert-manager \
  --namespace cert-manager \
  --create-namespace \
  --version v1.9.1

# Installing cert-manager will end with a message saying that the next step
# is to create some Issuers and/or ClusterIssuers.  That is indeed
# among the things that the kcp helm chart will do.

echo "Install KCP"

echo "Tooling:"
echo "Grafana: http://localhost:3333/"
echo "Prometheus: http://localhost:9091"
echo "KCP API Server: https://localhost:9443"
echo "KCP FrontProxy Server: https://localhost:9444"

# must be last as will be blocking
tilt up -f contrib/tilt/Tiltfile
