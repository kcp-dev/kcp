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

DEMO_ROOT="$(dirname "${BASH_SOURCE}")"

${DEMO_ROOT}/clusters/kind/createKindClusters.sh

TEMP_DIR=$(mktemp -d)
NGINX_DEPLOYMENT_YAML="${TEMP_DIR}/nginx-deployment.yaml"

# Pinned version of nginx-ingress-controller
VERSION="controller-v1.0.3"
# Get the nginx deployment yaml.
curl https://raw.githubusercontent.com/kubernetes/ingress-nginx/"${VERSION}"/deploy/static/provider/kind/deploy.yaml > "${NGINX_DEPLOYMENT_YAML}"
# We need the ingress controller to report back the internal ip address of the node instead of localhost.
sed -i'' -e "s/--publish-status-address=localhost/--report-node-internal-ip-address/g" "${NGINX_DEPLOYMENT_YAML}"

# Get all the created clusters kubeconfigs, and deploy the Ingress controller.
for kubeconfig in "${DEMO_ROOT}/clusters/kind"/*.kubeconfig;
do
 [ -f "$kubeconfig" ] || break
 KUBECONFIG=${kubeconfig} kubectl apply -f "${NGINX_DEPLOYMENT_YAML}"
done

