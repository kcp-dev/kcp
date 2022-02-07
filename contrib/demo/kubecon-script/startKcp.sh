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

DEMO_DIR="$( dirname "${BASH_SOURCE[0]}" )"
# shellcheck source=../.setupEnv
source "${DEMO_DIR}"/../.setupEnv
# shellcheck source=../.startUtils
source "${DEMOS_DIR}"/.startUtils
setupTraps "$0"

KUBECONFIG=${KCP_DATA_DIR}/.kcp/admin.kubeconfig

"${DEMOS_DIR}"/startKcp.sh \
    --install-cluster-controller \
    --push-mode \
    --auto-publish-apis=true \
    --resources-to-sync deployments.apps \
    --listen=127.0.0.1:6443

echo ""
echo "Starting Deployment Splitter"
"${KCP_DIR}"/bin/deployment-splitter -kubeconfig="${KUBECONFIG}" &> deployment-splitter.log &
SPLIT_PID=$!
echo "Deployment Splitter started: $SPLIT_PID"

echo ""
echo "Use ctrl-C to stop all components"
echo ""

tail -f deployment-splitter.log  &

wait
