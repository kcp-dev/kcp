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

source ${DEMOS_DIR}/.startUtils
setupTraps $0

CURRENT_DIR="$(pwd)"

KUBECONFIG=${KCP_DATA_DIR}/.kcp/admin.kubeconfig
export KCP_LISTEN_ADDR="127.0.0.1:6443"

${DEMOS_DIR}/startKcpAndClusterController.sh --auto-publish-apis=false --resources-to-sync deployments.apps &

wait_command "grep 'Serving securely' ${CURRENT_DIR}/kcp.log"

echo ""
echo "Use ctrl-C to stop all components"
echo ""

wait
