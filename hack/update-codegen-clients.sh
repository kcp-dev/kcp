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

KCP_GVS="workload:v1alpha1 apiresource:v1alpha1 tenancy:v1alpha1 tenancy:v1beta1 apis:v1alpha1 scheduling:v1alpha1"

bash "${CODEGEN_PKG}"/generate-groups.sh "deepcopy,client" \
  github.com/kcp-dev/kcp/pkg/client github.com/kcp-dev/kcp/pkg/apis \
  "${KCP_GVS}" \
  --go-header-file "${SCRIPT_ROOT}"/hack/boilerplate/boilerplate.generatego.txt \
  --output-base "${SCRIPT_ROOT}" \
  --trim-path-prefix github.com/kcp-dev/kcp

"${CODE_GENERATOR}" "informer,lister" \
  --clientset-api-path github.com/kcp-dev/kcp/pkg/client/clientset/versioned \
  --input-dir "${SCRIPT_ROOT}/pkg/apis" \
  --group-versions $(echo ${KCP_GVS} | sed 's/ / --group-versions /g') \
  --go-header-file "${SCRIPT_ROOT}"/hack/boilerplate/boilerplate.generatego.txt \
  --output-dir "${SCRIPT_ROOT}/pkg/client"

bash "${CODEGEN_PKG}"/generate-groups.sh "deepcopy" \
  github.com/kcp-dev/kcp/third_party/conditions/client github.com/kcp-dev/kcp/third_party/conditions/apis \
  "conditions:v1alpha1" \
  --go-header-file "${SCRIPT_ROOT}"/hack/boilerplate/boilerplate.generatego.txt \
  --output-base "${SCRIPT_ROOT}" \
  --trim-path-prefix github.com/kcp-dev/kcp

TEST_GVS="wildwest:v1alpha1"

bash "${CODEGEN_PKG}"/generate-groups.sh "deepcopy,client" \
  github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis \
  "${TEST_GVS}" \
  --go-header-file "${SCRIPT_ROOT}"/hack/boilerplate/boilerplate.generatego.txt \
  --output-base "${SCRIPT_ROOT}" \
  --trim-path-prefix github.com/kcp-dev/kcp

"${CODE_GENERATOR}" "informer,lister" \
  --clientset-api-path github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/clientset/versioned \
  --input-dir "${SCRIPT_ROOT}/test/e2e/fixtures/wildwest/apis" \
  --group-versions $(echo ${TEST_GVS} | sed 's/ / --group-versions /g') \
  --go-header-file "${SCRIPT_ROOT}"/hack/boilerplate/boilerplate.generatego.txt \
  --output-dir "${SCRIPT_ROOT}/test/e2e/fixtures/wildwest/client"

go install "${CODEGEN_PKG}"/cmd/openapi-gen

"$GOPATH"/bin/openapi-gen  --input-dirs github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1 \
--input-dirs github.com/kcp-dev/kcp/pkg/apis/apiresource/v1alpha1 \
--input-dirs github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1 \
--input-dirs github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1 \
--input-dirs github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1 \
--input-dirs github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1 \
--input-dirs github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1 \
--input-dirs k8s.io/apimachinery/pkg/apis/meta/v1,k8s.io/apimachinery/pkg/runtime,k8s.io/apimachinery/pkg/version \
--output-package github.com/kcp-dev/kcp/pkg/openapi -O zz_generated.openapi \
--go-header-file ./hack/../hack/boilerplate/boilerplate.generatego.txt \
--output-base "${SCRIPT_ROOT}" \
--trim-path-prefix github.com/kcp-dev/kcp
