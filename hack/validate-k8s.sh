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

set -o errexit
set -o nounset
set -o pipefail
set -o xtrace

# We must never use anything but our forks of the Kubernetes libraries.
invalid="$( go mod edit -json | jq '.Replace[] | select(.Old.Path | startswith("k8s.io/")) | select(.New.Path | startswith("github.com/kcp-dev/") | not) | .Old.Path' --raw-output )"
if [[ -n "${invalid}" ]]; then
  echo "[ERROR] The following k8s.io/* requirements have been replaced with something *other* than the kcp-dev fork: ${invalid}"
  exit 1
fi
