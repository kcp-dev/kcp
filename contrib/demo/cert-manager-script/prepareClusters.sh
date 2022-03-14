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

export DEMO_DIR="$( dirname "${BASH_SOURCE[0]}" )"
source "${DEMO_DIR}"/../.setupEnv

${DEMOS_DIR}/clusters/kind/createKindClusters.sh
NGINX_DEPLOYMENT_YAML="${DEMO_DIR}/nginx-ingress.yaml"

# Get all the created clusters kubeconfigs, and deploy the Ingress controller.
for kubeconfig in "${DEMOS_DIR}/clusters/kind"/*.kubeconfig;
do
 [ -f "$kubeconfig" ] || break
 KUBECONFIG=${kubeconfig} kubectl apply -f "${NGINX_DEPLOYMENT_YAML}"
done
