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

export GOPATH=$(go env GOPATH)

SCRIPT_ROOT=$(dirname "${BASH_SOURCE[0]}")/..
CODEGEN_PKG=${CODEGEN_PKG:-$(cd "${SCRIPT_ROOT}"; go list -f '{{.Dir}}' -m k8s.io/code-generator)}

bash "${CODEGEN_PKG}"/generate-groups.sh "deepcopy,client,informer,lister" \
  github.com/kcp-dev/kcp/pkg/client github.com/kcp-dev/kcp/pkg/apis \
  "cluster:v1alpha1 apiresource:v1alpha1 tenancy:v1alpha1 tenancy:v1beta1" \
  --go-header-file "${SCRIPT_ROOT}"/hack/boilerplate/boilerplate.generatego.txt --output-base ${GOPATH}/src

bash "${CODEGEN_PKG}"/generate-groups.sh "deepcopy" \
  github.com/kcp-dev/kcp/third_party/conditions/client github.com/kcp-dev/kcp/third_party/conditions/apis \
  "conditions:v1alpha1" \
  --go-header-file "${SCRIPT_ROOT}"/hack/boilerplate/boilerplate.generatego.txt --output-base ${GOPATH}/src

bash "${CODEGEN_PKG}"/generate-groups.sh "deepcopy,client,informer,lister" \
  github.com/kcp-dev/kcp/test/e2e/reconciler/cluster/client github.com/kcp-dev/kcp/test/e2e/reconciler/cluster/apis \
  "wildwest:v1alpha1" \
  --go-header-file "${SCRIPT_ROOT}"/hack/boilerplate/boilerplate.generatego.txt --output-base ${GOPATH}/src

go install "${CODEGEN_PKG}"/cmd/openapi-gen

"$GOPATH"/bin/openapi-gen  --input-dirs github.com/kcp-dev/kcp/pkg/apis/cluster/v1alpha1 \
--input-dirs github.com/kcp-dev/kcp/pkg/apis/apiresource/v1alpha1 \
--input-dirs github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1 \
--input-dirs github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1 \
--input-dirs github.com/kcp-dev/kcp/third_party/conditions/apis/conditions/v1alpha1 \
--input-dirs k8s.io/apimachinery/pkg/apis/meta/v1,k8s.io/apimachinery/pkg/runtime,k8s.io/apimachinery/pkg/version \
--output-package github.com/kcp-dev/kcp/pkg/openapi -O zz_generated.openapi \
--go-header-file ./hack/../hack/boilerplate/boilerplate.generatego.txt --output-base "$GOPATH"/src
