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

set -euo pipefail

hostname="$(yq '.externalHostname' < contrib/tilt/kcp-values.yaml)"

cat << EOF > kcp.kubeconfig
apiVersion: v1
kind: Config
clusters:
  - cluster:
      insecure-skip-tls-verify: true
      server: "https://$hostname:6443/clusters/root"
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
  - name: oidc
    user:
      exec:
        apiVersion: client.authentication.k8s.io/v1beta1
        args:
        - oidc-login
        - get-token
        - --oidc-issuer-url=https://idp.dev.local:6443
        - --oidc-client-id=kcp-dev
        - --oidc-client-secret=Z2Fyc2lha2FsYmlzdmFuZGVuekWplCg==
        - --oidc-extra-scope=email
        - --insecure-skip-tls-verify=true
        command: kubectl
        env: null
        interactiveMode: IfAvailable
        provideClusterInfo: false
EOF

echo "Kubeconfig file created at kcp.kubeconfig"
echo ""
echo "export KUBECONFIG=kcp.kubeconfig"
echo ""
