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

set -o errexit
set -o nounset
set -o pipefail
set -o xtrace

# The normal flow for updating a replaced dependency would look like:
# $ go mod edit -replace old=new@branch
# $ go mod tidy
# However, we don't know all of the specific packages that we pull in from the staging repos,
# nor do we want to update this script when that set changes. Therefore, we just look up the
# version of the k8s.io/kubernetes replacement and replace all modules at that version, since
# we know we will always want to use a self-consistent set of modules from our fork.
# Note: setting GOPROXY=direct allows us to bump very quickly after the fork has been committed to.
GITHUB_USER=${GITHUB_USER:-kcp-dev}
GITHUB_REPO=${GITHUB_REPO:-kubernetes}
BRANCH=${BRANCH:-kcp-feature-logical-clusters-1.24-v3}

current_version="$( GOPROXY=direct go mod edit -json | jq '.Replace[] | select(.Old.Path=="k8s.io/kubernetes") | .New.Version' --raw-output )"

# equivalent to go mod edit -replace
is_gnu_sed() { sed --version >/dev/null 2>&1; }
if is_gnu_sed; then
  SED="sed -i"
else
  SED="sed -i ''"
fi
${SED} -e "s|${current_version}|${BRANCH}|g" -E -e "s,=> github.com/kcp-dev/kubernetes,=> github.com/${GITHUB_USER}/${GITHUB_REPO},g" go.mod

GOPROXY=direct go mod tidy
