#!/usr/bin/env bash

# Copyright 2022 The KCP Authors.
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

TYPE_SPEED=${TYPE_SPEED:-150}
#NO_WAIT=true

DEMO_DIR="$(dirname "${BASH_SOURCE[0]}")"
source "${DEMO_DIR}"/../.setupEnv

# shellcheck source=../demo-magic
. "${DEMOS_DIR}"/demo-magic

DEMO_PROMPT="☸️ $ "

function pause() {
  if [[ "${NO_WAIT}" = "true" ]]; then
    sleep 2
  else
    if [[ -n "${1-}" ]]; then
      sleep "$1"
    else
      wait
    fi
  fi
}

function c() {
  local comment="$*"
  if command -v fold &> /dev/null; then
    comment=$(echo "$comment" | fold -w "${cols:-100}")
  fi
  p "# $comment"
}

export KUBECONFIG=${KUBECONFIG:-${KCP_DIR}/.kcp/admin.kubeconfig}

if ! kubectl get namespaces &>/dev/null; then
  echo "kcp server not started, run 'bin/kcp start'"
  exit 1
fi

pe "kubectl config use-context root"
pe "kubectl kcp workspace create demo --type Organization"
sleep 1
pe "kubectl kcp workspace use demo"

clear
