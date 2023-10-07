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

# TODO: use generate-groups.sh directly instead once https://github.com/kubernetes/kubernetes/pull/114987 is available
go install "${CODEGEN_PKG}"/cmd/applyconfiguration-gen
go install "${CODEGEN_PKG}"/cmd/client-gen

"$GOPATH"/bin/applyconfiguration-gen \
  --input-dirs github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1 \
  --input-dirs github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1 \
  --input-dirs github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1 \
  --input-dirs github.com/kcp-dev/kcp/sdk/apis/topology/v1alpha1 \
  --input-dirs github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/apis/conditions/v1alpha1 \
  --input-dirs k8s.io/apimachinery/pkg/apis/meta/v1,k8s.io/apimachinery/pkg/runtime,k8s.io/apimachinery/pkg/version \
  --output-package github.com/kcp-dev/kcp/sdk/client/applyconfiguration \
  --go-header-file ./hack/../hack/boilerplate/boilerplate.generatego.txt \
  --output-base "${SCRIPT_ROOT}" \
  --trim-path-prefix github.com/kcp-dev/kcp

"$GOPATH"/bin/client-gen \
  --input github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1 \
  --input github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1 \
  --input github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1 \
  --input github.com/kcp-dev/kcp/sdk/apis/topology/v1alpha1 \
  --input-base="" \
  --apply-configuration-package=github.com/kcp-dev/kcp/sdk/client/applyconfiguration \
  --clientset-name "versioned"  \
  --output-package github.com/kcp-dev/kcp/sdk/client/clientset \
  --go-header-file ./hack/../hack/boilerplate/boilerplate.generatego.txt \
  --output-base "${SCRIPT_ROOT}" \
  --trim-path-prefix github.com/kcp-dev/kcp

bash "${CODEGEN_PKG}"/generate-groups.sh "deepcopy" \
  github.com/kcp-dev/kcp/sdk/client github.com/kcp-dev/kcp/sdk/apis \
  "core:v1alpha1 tenancy:v1alpha1 apis:v1alpha1 topology:v1alpha1" \
  --go-header-file "${SCRIPT_ROOT}"/hack/boilerplate/boilerplate.generatego.txt \
  --output-base "${SCRIPT_ROOT}" \
  --trim-path-prefix github.com/kcp-dev/kcp

pushd ./sdk/apis
${CODE_GENERATOR} \
  "client:outputPackagePath=github.com/kcp-dev/kcp/sdk/client,apiPackagePath=github.com/kcp-dev/kcp/sdk/apis,singleClusterClientPackagePath=github.com/kcp-dev/kcp/sdk/client/clientset/versioned,singleClusterApplyConfigurationsPackagePath=github.com/kcp-dev/kcp/sdk/client/applyconfiguration,headerFile=${BOILERPLATE_HEADER}" \
  "lister:apiPackagePath=github.com/kcp-dev/kcp/sdk/apis,headerFile=${BOILERPLATE_HEADER}" \
  "informer:outputPackagePath=github.com/kcp-dev/kcp/sdk/client,singleClusterClientPackagePath=github.com/kcp-dev/kcp/sdk/client/clientset/versioned,apiPackagePath=github.com/kcp-dev/kcp/sdk/apis,headerFile=${BOILERPLATE_HEADER}" \
  "paths=./..." \
  "output:dir=./../client"
popd

"$GOPATH"/bin/applyconfiguration-gen \
  --input-dirs github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest/v1alpha1 \
  --input-dirs k8s.io/apimachinery/pkg/apis/meta/v1 \
  --output-package github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/applyconfiguration \
  --go-header-file "${SCRIPT_ROOT}"/hack/boilerplate/boilerplate.generatego.txt \
  --output-base "${SCRIPT_ROOT}" \
  --trim-path-prefix github.com/kcp-dev/kcp

"$GOPATH"/bin/client-gen \
  --input github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest/v1alpha1 \
  --input-base="" \
  --apply-configuration-package=github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/applyconfiguration \
  --clientset-name "versioned"  \
  --output-package github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/clientset \
  --go-header-file ./hack/../hack/boilerplate/boilerplate.generatego.txt \
  --output-base "${SCRIPT_ROOT}" \
  --trim-path-prefix github.com/kcp-dev/kcp

bash "${CODEGEN_PKG}"/generate-groups.sh "deepcopy" \
  github.com/kcp-dev/kcp/third_party/conditions/client github.com/kcp-dev/kcp/third_party/conditions/apis \
  "conditions:v1alpha1" \
  --go-header-file "${SCRIPT_ROOT}"/hack/boilerplate/boilerplate.generatego.txt \
  --output-base "${SCRIPT_ROOT}" \
  --trim-path-prefix github.com/kcp-dev/kcp

bash "${CODEGEN_PKG}"/generate-groups.sh "deepcopy" \
  github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis \
  "wildwest:v1alpha1" \
  --go-header-file "${SCRIPT_ROOT}"/hack/boilerplate/boilerplate.generatego.txt \
  --output-base "${SCRIPT_ROOT}" \
  --trim-path-prefix github.com/kcp-dev/kcp

pushd ./test/e2e/fixtures/wildwest/apis
${CODE_GENERATOR} \
  "client:outputPackagePath=github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client,apiPackagePath=github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis,singleClusterClientPackagePath=github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/clientset/versioned,singleClusterApplyConfigurationsPackagePath=github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/applyconfiguration,headerFile=${BOILERPLATE_HEADER}" \
  "lister:apiPackagePath=github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis,headerFile=${BOILERPLATE_HEADER}" \
  "informer:outputPackagePath=github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client,singleClusterClientPackagePath=github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/clientset/versioned,apiPackagePath=github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis,headerFile=${BOILERPLATE_HEADER}" \
  "paths=./..." \
  "output:dir=./../client"
popd

go install "${CODEGEN_PKG}"/cmd/openapi-gen

"$GOPATH"/bin/openapi-gen \
  --input-dirs github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1 \
  --input-dirs github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1 \
  --input-dirs github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1 \
  --input-dirs github.com/kcp-dev/kcp/sdk/apis/topology/v1alpha1 \
  --input-dirs github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/apis/conditions/v1alpha1 \
  --input-dirs k8s.io/apimachinery/pkg/apis/meta/v1,k8s.io/apimachinery/pkg/runtime,k8s.io/apimachinery/pkg/version \
  --output-package github.com/kcp-dev/kcp/pkg/openapi -O zz_generated.openapi \
  --go-header-file ./hack/../hack/boilerplate/boilerplate.generatego.txt \
  --output-base "${SCRIPT_ROOT}" \
  --trim-path-prefix github.com/kcp-dev/kcp
