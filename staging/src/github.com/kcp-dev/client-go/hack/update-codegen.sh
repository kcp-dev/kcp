#!/usr/bin/env bash

# Copyright 2022 The KCP Authors.
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

CODEGEN_PKG="$(go list -f '{{.Dir}}' -m k8s.io/code-generator)"
source "$CODEGEN_PKG/kube_codegen.sh"

CLUSTER_CODEGEN_PKG="$(go list -f '{{.Dir}}' -m github.com/kcp-dev/code-generator/v3)"
source "$CLUSTER_CODEGEN_PKG/cluster_codegen.sh"

make clean-generated

cluster::codegen::gen_client \
    --boilerplate ./hack/boilerplate/boilerplate.go.txt \
    --with-watch \
    --output-dir . \
    --output-pkg github.com/kcp-dev/client-go \
    --versioned-clientset-dir kubernetes \
    --versioned-clientset-pkg github.com/kcp-dev/client-go/kubernetes \
    --single-cluster-versioned-clientset-pkg k8s.io/client-go/kubernetes \
    --single-cluster-applyconfigurations-pkg k8s.io/client-go/applyconfigurations \
    --single-cluster-informers-pkg k8s.io/client-go/informers \
    --single-cluster-listers-pkg k8s.io/client-go/listers \
    --exclude-group-versions "imagepolicy/v1alpha1" \
    --plural-exceptions "Endpoints:Endpoints" \
    "$(go list -m -json k8s.io/api | jq --raw-output .Dir)"

cluster::codegen::gen_client \
    --boilerplate ./hack/boilerplate/boilerplate.go.txt \
    --output-dir apiextensions \
    --output-pkg github.com/kcp-dev/client-go/apiextensions \
    --with-watch \
    --versioned-clientset-pkg github.com/kcp-dev/client-go/apiextensions/client \
    --versioned-clientset-dir apiextensions/client \
    --single-cluster-versioned-clientset-pkg k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset \
    --single-cluster-applyconfigurations-pkg k8s.io/apiextensions-apiserver/pkg/client/applyconfiguration \
    --single-cluster-informers-pkg k8s.io/apiextensions-apiserver/pkg/client/informers/externalversions \
    --single-cluster-listers-pkg k8s.io/apiextensions-apiserver/pkg/client/listers \
    "$(go list -m -json k8s.io/apiextensions-apiserver | jq --raw-output .Dir)/pkg/apis"
