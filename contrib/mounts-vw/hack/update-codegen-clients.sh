#!/usr/bin/env bash

# Copyright 2023 The KCP Authors.
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
OPENAPI_PKG=${OPENAPI_PKG:-$(cd "${SCRIPT_ROOT}"; go list -f '{{.Dir}}' -m k8s.io/kube-openapi)}


# TODO: use generate-groups.sh directly instead once https://github.com/kubernetes/kubernetes/pull/114987 is available
go install "${CODEGEN_PKG}"/cmd/applyconfiguration-gen
go install "${CODEGEN_PKG}"/cmd/client-gen

# TODO: This is hack to allow CI to pass
chmod +x "${CODEGEN_PKG}"/generate-internal-groups.sh

source "${CODEGEN_PKG}/kube_codegen.sh"

"$GOPATH"/bin/applyconfiguration-gen \
  --go-header-file ./hack/boilerplate/boilerplate.generatego.txt \
  --output-pkg github.com/kcp-dev/kcp/contrib/mounts-vw/client/applyconfiguration \
  --output-dir "${SCRIPT_ROOT}/client/applyconfiguration" \
  github.com/kcp-dev/kcp/contrib/mounts-vw/apis/targets/v1alpha1 \
  github.com/kcp-dev/kcp/contrib/mounts-vw/apis/mounts/v1alpha1 \
  github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/apis/conditions/v1alpha1 \
  k8s.io/apimachinery/pkg/apis/meta/v1 \
  k8s.io/apimachinery/pkg/runtime \
  k8s.io/apimachinery/pkg/version

"$GOPATH"/bin/client-gen \
  --input github.com/kcp-dev/kcp/contrib/mounts-vw/apis/targets/v1alpha1 \
 --input github.com/kcp-dev/kcp/contrib/mounts-vw/apis/mounts/v1alpha1 \
  --input-base="" \
  --apply-configuration-package=github.com/kcp-dev/kcp/contrib/mounts-vw/client/applyconfiguration \
  --clientset-name "versioned"  \
  --output-pkg github.com/kcp-dev/kcp/contrib/mounts-vw/client/clientset \
  --go-header-file ./hack/boilerplate/boilerplate.generatego.txt \
  --output-dir "${SCRIPT_ROOT}/client/clientset"

kube::codegen::gen_helpers \
  --boilerplate "${SCRIPT_ROOT}"/hack/boilerplate/boilerplate.generatego.txt \
  ./apis

echo "$BOILERPLATE_HEADER"
pushd ./apis
${CODE_GENERATOR} \
  "client:outputPackagePath=github.com/kcp-dev/kcp/contrib/mounts-vw/client,apiPackagePath=github.com/kcp-dev/kcp/contrib/mounts-vw/apis,singleClusterClientPackagePath=github.com/kcp-dev/kcp/contrib/mounts-vw/client/clientset/versioned,singleClusterApplyConfigurationsPackagePath=github.com/kcp-dev/kcp/contrib/mounts-vw/client/applyconfiguration,headerFile=${BOILERPLATE_HEADER}" \
  "lister:apiPackagePath=github.com/kcp-dev/kcp/contrib/mounts-vw/apis,headerFile=${BOILERPLATE_HEADER}" \
  "informer:outputPackagePath=github.com/kcp-dev/kcp/contrib/mounts-vw/client,singleClusterClientPackagePath=github.com/kcp-dev/kcp/contrib/mounts-vw/client/clientset/versioned,apiPackagePath=github.com/kcp-dev/kcp/contrib/mounts-vw/apis,headerFile=${BOILERPLATE_HEADER}" \
  "paths=./..." \
  "output:dir=./../client"
popd

go install "${OPENAPI_PKG}"/cmd/openapi-gen

"$GOPATH"/bin/openapi-gen \
  --output-pkg github.com/kcp-dev/kcp/contrib/mounts-vw/openapi \
  --go-header-file ./hack/boilerplate/boilerplate.generatego.txt \
  --output-dir "${SCRIPT_ROOT}/openapi" \
  github.com/kcp-dev/kcp/contrib/mounts-vw/apis/targets/v1alpha1 \
  github.com/kcp-dev/kcp/contrib/mounts-vw/apis/mounts/v1alpha1 \
  github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/apis/conditions/v1alpha1 \
  k8s.io/apimachinery/pkg/apis/meta/v1 \
  k8s.io/apimachinery/pkg/runtime \
  k8s.io/apimachinery/pkg/version
