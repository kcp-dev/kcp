#!/usr/bin/env bash

# Copyright 2025 The KCP Authors.
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

set -euo pipefail

outfile="$PWD/frontproxy.kubeconfig"

cd "$(dirname "$0")"

hostname="$(yq '.externalHostname' < ./kcp-values.yaml)"

kubectl --context kind-kcp -n kcp-certs apply -f- <<EOF
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: cluster-admin-client-cert
spec:
  commonName: cluster-admin
  issuerRef:
    name: certs-front-proxy-client-issuer
  privateKey:
    algorithm: RSA
    size: 2048
  secretName: cluster-admin-client-cert
  subject:
    organizations:
    - system:kcp:admin
  usages:
  - client auth
EOF

kubectl --context kind-kcp -n kcp-certs wait certificate/cluster-admin-client-cert --for=condition=Ready

kubectl --context kind-kcp -n kcp-certs get secret proxy-front-proxy-cert -o=jsonpath='{.data.tls\.crt}' | base64 -d > ca.crt
kubectl --context kind-kcp -n kcp-certs get secret cluster-admin-client-cert -o=jsonpath='{.data.tls\.crt}' | base64 -d > client.crt
kubectl --context kind-kcp -n kcp-certs get secret cluster-admin-client-cert -o=jsonpath='{.data.tls\.key}' | base64 -d > client.key
chmod 0600 ca.crt client.crt client.key

kubectl --kubeconfig="$outfile" config set-cluster root --server=https://$hostname:8443/clusters/root --certificate-authority="$(realpath ca.crt)"
kubectl --kubeconfig="$outfile" config set-credentials kcp-admin --client-certificate="$(realpath client.crt)" --client-key="$(realpath client.key)"
kubectl --kubeconfig="$outfile" config set-context root --cluster=root --user=kcp-admin
kubectl --kubeconfig="$outfile" config use-context root
echo "Kubeconfig file created at '$outfile'"
