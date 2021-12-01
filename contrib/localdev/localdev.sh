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

# localdev.sh overrides the go.mod file with a set of relative directories
# so you can develop with a local version of Kubernetes for quick iteration.
# To do this you must check out k/k from kcp-dev/kubernetes from the
# feature-logical-clusters branch locally and have it be in the expected
# GOPATH location (../../../k8s.io/kubernetes).
#
# This script should eventually be paired with vendoring scripts for
# maintaining the set of repos and coordinating bumps.

set -o errexit
set -o nounset
set -o pipefail

OS_ROOT="$(dirname "${BASH_SOURCE}")/../.."

sed -i -e 's:=>  *github\.com/kcp-dev/\(kubernetes[^ ]*\) ..*:=> ../../../k8s.io/\1:' "${OS_ROOT}/go.mod"
