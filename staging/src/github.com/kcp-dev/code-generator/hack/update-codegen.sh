#!/usr/bin/env bash

# Copyright 2025 The KCP Authors.
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

if [[ -z "${MAKELEVEL:-}" ]]; then
  echo 'You must invoke this script via make'
  exit 1
fi

SCRIPT_ROOT=$(dirname "${BASH_SOURCE[0]}")/..
CODEGEN_PKG=${CODEGEN_PKG:-$(cd "${SCRIPT_ROOT}"; go list -f '{{.Dir}}' -m k8s.io/code-generator)}

source "${CODEGEN_PKG}/kube_codegen.sh"
source cluster_codegen.sh

pushd ./examples

# Generate deepcopy functions
kube::codegen::gen_helpers \
  --boilerplate ./../hack/boilerplate/examples/boilerplate.generatego.txt \
  ./pkg/apis

# Generate standard clientset, listers and informers
rm -rf pkg/generated
mkdir -p pkg/generated/{clientset,applyconfigurations,listers,informers}

kube::codegen::gen_client \
  --boilerplate ./../hack/boilerplate/examples/boilerplate.generatego.txt \
  --output-dir pkg/generated \
  --output-pkg acme.corp/pkg/generated \
  --with-applyconfig \
  --applyconfig-name applyconfigurations \
  --with-watch \
  ./pkg/apis

# Generate cluster-aware clients, informers and listers using generated single-cluster code
rm -rf pkg/kcpexisting
mkdir -p pkg/kcpexisting/clients/{clientset/versioned,listers,informers/externalversions}

cluster::codegen::gen_client \
  --boilerplate ../hack/boilerplate/examples/boilerplate.generatego.txt \
  --output-dir pkg/kcpexisting/clients \
  --output-pkg acme.corp/pkg/kcpexisting/clients \
  --versioned-clientset-dir pkg/kcpexisting/clients/clientset/versioned \
  --versioned-clientset-pkg acme.corp/pkg/kcpexisting/clients/clientset/versioned \
  --informers-dir pkg/kcpexisting/clients/informers/externalversions \
  --informers-pkg acme.corp/pkg/kcpexisting/clients/informers/externalversions \
  --with-watch \
  --single-cluster-versioned-clientset-pkg acme.corp/pkg/generated/clientset/versioned \
  --single-cluster-applyconfigurations-pkg acme.corp/pkg/generated/applyconfigurations \
  --single-cluster-listers-pkg acme.corp/pkg/generated/listers \
  --single-cluster-informers-pkg acme.corp/pkg/generated/informers/externalversions \
  pkg/apis

# Generate cluster-aware clients, informers and listers assuming no single-cluster listers or informers
rm -rf pkg/kcp
mkdir -p pkg/kcp/clients/{clientset/versioned,listers,informers/externalversions}

cluster::codegen::gen_client \
  --boilerplate ../hack/boilerplate/examples/boilerplate.generatego.txt \
  --output-dir pkg/kcp/clients \
  --output-pkg acme.corp/pkg/kcp/clients \
  --versioned-clientset-dir pkg/kcp/clients/clientset/versioned \
  --versioned-clientset-pkg acme.corp/pkg/kcp/clients/clientset/versioned \
  --informers-dir pkg/kcp/clients/informers/externalversions \
  --informers-pkg acme.corp/pkg/kcp/clients/informers/externalversions \
  --with-watch \
  --single-cluster-versioned-clientset-pkg acme.corp/pkg/generated/clientset/versioned \
  --single-cluster-applyconfigurations-pkg acme.corp/pkg/generated/applyconfigurations \
  pkg/apis

popd
