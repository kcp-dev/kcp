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

"$GOPATH"/bin/applyconfiguration-gen \
  --go-header-file ./hack/boilerplate/boilerplate.generatego.txt \
  --output-pkg github.com/kcp-dev/kcp/contrib/kube-bind/clients/applyconfiguration \
  --output-dir "${SCRIPT_ROOT}/clients/applyconfiguration" \
  github.com/kube-bind/kube-bind/pkg/apis/kubebind/v1alpha1 \
  github.com/kube-bind/kube-bind/pkg/apis/third_party/conditions/apis/conditions/v1alpha1 \
  k8s.io/apimachinery/pkg/apis/meta/v1 \
  k8s.io/apimachinery/pkg/runtime \
  k8s.io/apimachinery/pkg/version


"$GOPATH"/bin/client-gen \
  --input github.com/kube-bind/kube-bind/pkg/apis/kubebind/v1alpha1 \
  --input-base="" \
  --apply-configuration-package=github.com/kcp-dev/kcp/contrib/kube-bind/clients/applyconfiguration \
  --clientset-name "versioned"  \
  --output-pkg github.com/kcp-dev/kcp/contrib/kube-bind/clients/clientset \
  --go-header-file ./hack/boilerplate/boilerplate.generatego.txt \
  --output-dir "${SCRIPT_ROOT}/clients/clientset"

bash "${CODEGEN_PKG}"/kube_codegen.sh "deepcopy" \
  github.com/kcp-dev/kcp/contrib/kube-bind/clients github.com/kube-bind/kube-bind/pkg/apis \
  "proxy:v1alpha1 tenancy:v1alpha1" \
  --go-header-file ./hack/boilerplate/boilerplate.generatego.txt \
  --output-base "${SCRIPT_ROOT}" \
  --trim-path-prefix github.com/faroshq/cluster-proxy

