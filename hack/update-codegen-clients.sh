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
pushd "${SCRIPT_ROOT}"
BOILERPLATE_HEADER="$( pwd )/hack/boilerplate/boilerplate.go.txt"
popd
CODEGEN_PKG=${CODEGEN_PKG:-$(cd "${SCRIPT_ROOT}"; go list -f '{{.Dir}}' -m k8s.io/code-generator)}

bash "${CODEGEN_PKG}"/generate-groups.sh "deepcopy,client" \
  github.com/kcp-dev/kcp/pkg/client github.com/kcp-dev/kcp/pkg/apis \
  "core:v1alpha1 workload:v1alpha1 apiresource:v1alpha1 tenancy:v1alpha1 tenancy:v1beta1 apis:v1alpha1 scheduling:v1alpha1 topology:v1alpha1" \
  --go-header-file "${SCRIPT_ROOT}"/hack/boilerplate/boilerplate.generatego.txt \
  --output-base "${SCRIPT_ROOT}" \
  --trim-path-prefix github.com/kcp-dev/kcp

pushd ./pkg/apis
${CODE_GENERATOR} \
  "client:outputPackagePath=github.com/kcp-dev/kcp/pkg/client,apiPackagePath=github.com/kcp-dev/kcp/pkg/apis,singleClusterClientPackagePath=github.com/kcp-dev/kcp/pkg/client/clientset/versioned,headerFile=${BOILERPLATE_HEADER}" \
  "lister:apiPackagePath=github.com/kcp-dev/kcp/pkg/apis,headerFile=${BOILERPLATE_HEADER}" \
  "informer:outputPackagePath=github.com/kcp-dev/kcp/pkg/client,singleClusterClientPackagePath=github.com/kcp-dev/kcp/pkg/client/clientset/versioned,apiPackagePath=github.com/kcp-dev/kcp/pkg/apis,headerFile=${BOILERPLATE_HEADER}" \
  "paths=./..." \
  "output:dir=./../client"
popd

bash "${CODEGEN_PKG}"/generate-groups.sh "deepcopy" \
  github.com/kcp-dev/kcp/third_party/conditions/client github.com/kcp-dev/kcp/third_party/conditions/apis \
  "conditions:v1alpha1" \
  --go-header-file "${SCRIPT_ROOT}"/hack/boilerplate/boilerplate.generatego.txt \
  --output-base "${SCRIPT_ROOT}" \
  --trim-path-prefix github.com/kcp-dev/kcp

bash "${CODEGEN_PKG}"/generate-groups.sh "deepcopy,client" \
  github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis \
  "wildwest:v1alpha1" \
  --go-header-file "${SCRIPT_ROOT}"/hack/boilerplate/boilerplate.generatego.txt \
  --output-base "${SCRIPT_ROOT}" \
  --trim-path-prefix github.com/kcp-dev/kcp

pushd ./test/e2e/fixtures/wildwest/apis
${CODE_GENERATOR} \
  "client:outputPackagePath=github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client,apiPackagePath=github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis,singleClusterClientPackagePath=github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/clientset/versioned,headerFile=${BOILERPLATE_HEADER}" \
  "lister:apiPackagePath=github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis,headerFile=${BOILERPLATE_HEADER}" \
  "informer:outputPackagePath=github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client,singleClusterClientPackagePath=github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/clientset/versioned,apiPackagePath=github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis,headerFile=${BOILERPLATE_HEADER}" \
  "paths=./..." \
  "output:dir=./../client"
popd

go install "${CODEGEN_PKG}"/cmd/openapi-gen

"$GOPATH"/bin/openapi-gen  --input-dirs github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1 \
--input-dirs github.com/kcp-dev/kcp/pkg/apis/core/v1alpha1 \
--input-dirs github.com/kcp-dev/kcp/pkg/apis/apiresource/v1alpha1 \
--input-dirs github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1 \
--input-dirs github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1 \
--input-dirs github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1 \
--input-dirs github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1 \
--input-dirs github.com/kcp-dev/kcp/pkg/apis/topology/v1alpha1 \
--input-dirs github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1 \
--input-dirs k8s.io/apimachinery/pkg/apis/meta/v1,k8s.io/apimachinery/pkg/runtime,k8s.io/apimachinery/pkg/version \
--output-package github.com/kcp-dev/kcp/pkg/openapi -O zz_generated.openapi \
--go-header-file ./hack/../hack/boilerplate/boilerplate.generatego.txt \
--output-base "${SCRIPT_ROOT}" \
--trim-path-prefix github.com/kcp-dev/kcp
