#!/usr/bin/env bash

# Copyright 2021 The kcp Authors.
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
BOILERPLATE_HEADER="$( pwd )/hack/boilerplate/boilerplate.generatego.txt"
popd
SDK_PKG=${SDK_PKG:-$(cd "${SCRIPT_ROOT}"; go list -f '{{.Dir}}' -m github.com/kcp-dev/sdk)}
CODEGEN_PKG=${CODEGEN_PKG:-$(cd "${SCRIPT_ROOT}"; go list -f '{{.Dir}}' -m k8s.io/code-generator)}
CLUSTER_CODEGEN_PKG=${CLUSTER_CODEGEN_PKG:-$(cd "${SCRIPT_ROOT}"; go list -f '{{.Dir}}' -m github.com/kcp-dev/code-generator/v3)}
OPENAPI_PKG=${OPENAPI_PKG:-$(cd "${SCRIPT_ROOT}"; go list -f '{{.Dir}}' -m k8s.io/kube-openapi)}

go install "${CODEGEN_PKG}"/cmd/applyconfiguration-gen
go install "${CODEGEN_PKG}"/cmd/client-gen

# TODO: This is hack to allow CI to pass
chmod +x "${CODEGEN_PKG}"/generate-internal-groups.sh

source "${CODEGEN_PKG}/kube_codegen.sh"
source "${CLUSTER_CODEGEN_PKG}/cluster_codegen.sh"

rm -rf ${SDK_PKG}/client/{clientset,applyconfiguration,listers,informers}
mkdir -p ${SDK_PKG}/client/{clientset,applyconfiguration,listers,informers}

"$GOPATH"/bin/applyconfiguration-gen \
  --go-header-file "${BOILERPLATE_HEADER}" \
  --output-pkg github.com/kcp-dev/sdk/client/applyconfiguration \
  --output-dir "${SDK_PKG}/client/applyconfiguration" \
  github.com/kcp-dev/sdk/apis/core/v1alpha1 \
  github.com/kcp-dev/sdk/apis/tenancy/v1alpha1 \
  github.com/kcp-dev/sdk/apis/apis/v1alpha1 \
  github.com/kcp-dev/sdk/apis/apis/v1alpha2 \
  github.com/kcp-dev/sdk/apis/cache/v1alpha1 \
  github.com/kcp-dev/sdk/apis/topology/v1alpha1 \
  github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1 \
  k8s.io/apimachinery/pkg/apis/meta/v1 \
  k8s.io/apimachinery/pkg/runtime \
  k8s.io/apimachinery/pkg/version

"$GOPATH"/bin/client-gen \
  --go-header-file "${BOILERPLATE_HEADER}" \
  --output-pkg github.com/kcp-dev/sdk/client/clientset \
  --output-dir "${SDK_PKG}/client/clientset" \
  --input github.com/kcp-dev/sdk/apis/core/v1alpha1 \
  --input github.com/kcp-dev/sdk/apis/tenancy/v1alpha1 \
  --input github.com/kcp-dev/sdk/apis/apis/v1alpha1 \
  --input github.com/kcp-dev/sdk/apis/apis/v1alpha2 \
  --input github.com/kcp-dev/sdk/apis/topology/v1alpha1 \
  --input github.com/kcp-dev/sdk/apis/cache/v1alpha1 \
  --input-base="" \
  --apply-configuration-package=github.com/kcp-dev/sdk/client/applyconfiguration \
  --clientset-name "versioned"

kube::codegen::gen_helpers \
  --boilerplate "${BOILERPLATE_HEADER}" \
  ./staging/src/github.com/kcp-dev/sdk/apis

cd ./staging/src/github.com/kcp-dev/sdk
cluster::codegen::gen_client \
  --boilerplate "${BOILERPLATE_HEADER}" \
  --versioned-clientset-dir client/clientset/versioned/cluster \
  --versioned-clientset-pkg github.com/kcp-dev/sdk/client/clientset/versioned/cluster \
  --listers-dir client/listers \
  --listers-pkg github.com/kcp-dev/sdk/client/listers \
  --informers-dir client/informers/externalversions \
  --informers-pkg github.com/kcp-dev/sdk/client/informers/externalversions \
  --with-watch \
  --single-cluster-versioned-clientset-pkg github.com/kcp-dev/sdk/client/clientset/versioned \
  --single-cluster-applyconfigurations-pkg github.com/kcp-dev/sdk/client/applyconfiguration \
  apis
cd -

"$GOPATH"/bin/applyconfiguration-gen \
  --go-header-file "${BOILERPLATE_HEADER}" \
  --output-pkg github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/applyconfiguration \
  --output-dir "${SCRIPT_ROOT}/test/e2e/fixtures/wildwest/client/applyconfiguration" \
  github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest/v1alpha1 \
  k8s.io/apimachinery/pkg/apis/meta/v1

"$GOPATH"/bin/client-gen \
  --go-header-file "${BOILERPLATE_HEADER}" \
  --output-pkg github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/clientset \
  --output-dir "${SCRIPT_ROOT}/test/e2e/fixtures/wildwest/client/clientset" \
  --input github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest/v1alpha1 \
  --input-base="" \
  --apply-configuration-package=github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/applyconfiguration \
  --clientset-name "versioned"

kube::codegen::gen_helpers \
  --boilerplate "${BOILERPLATE_HEADER}" \
  ./staging/src/github.com/kcp-dev/sdk/third_party/conditions/apis

kube::codegen::gen_helpers \
  --boilerplate "${BOILERPLATE_HEADER}" \
  ./test/e2e/fixtures/wildwest/apis

cd ./test/e2e/fixtures/wildwest
cluster::codegen::gen_client \
  --boilerplate "${BOILERPLATE_HEADER}" \
  --versioned-clientset-dir client/clientset/versioned/cluster \
  --versioned-clientset-pkg github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/clientset/versioned/cluster \
  --listers-dir client/listers \
  --listers-pkg github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/listers \
  --informers-dir client/informers/externalversions \
  --informers-pkg github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/informers/externalversions \
  --with-watch \
  --single-cluster-versioned-clientset-pkg github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/clientset/versioned \
  --single-cluster-applyconfigurations-pkg github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/applyconfiguration \
  apis
cd -

go install "${OPENAPI_PKG}"/cmd/openapi-gen

"$GOPATH"/bin/openapi-gen \
  --go-header-file "${BOILERPLATE_HEADER}" \
  --output-pkg github.com/kcp-dev/kcp/pkg/openapi \
  --output-file zz_generated.openapi.go \
  --output-dir "${SCRIPT_ROOT}/pkg/openapi" \
  github.com/kcp-dev/sdk/apis/core/v1alpha1 \
  github.com/kcp-dev/sdk/apis/tenancy/v1alpha1 \
  github.com/kcp-dev/sdk/apis/apis/v1alpha1 \
  github.com/kcp-dev/sdk/apis/apis/v1alpha2 \
  github.com/kcp-dev/sdk/apis/topology/v1alpha1 \
  github.com/kcp-dev/sdk/apis/cache/v1alpha1 \
  github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1 \
  k8s.io/apimachinery/pkg/apis/meta/v1 \
  k8s.io/apimachinery/pkg/runtime \
  k8s.io/apimachinery/pkg/version
