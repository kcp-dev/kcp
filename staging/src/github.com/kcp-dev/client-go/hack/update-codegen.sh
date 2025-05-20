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

if [[ -z "${MAKELEVEL:-}" ]]; then
    echo 'You must invoke this script via make'
    exit 1
fi

${CODE_GENERATOR} \
  "client:externalOnly=true,standalone=true,outputPackagePath=github.com/kcp-dev/client-go,name=kubernetes,apiPackagePath=k8s.io/api,singleClusterClientPackagePath=k8s.io/client-go/kubernetes,singleClusterApplyConfigurationsPackagePath=k8s.io/client-go/applyconfigurations,headerFile=./hack/boilerplate/boilerplate.go.txt" \
  "lister:apiPackagePath=k8s.io/api,singleClusterListerPackagePath=k8s.io/client-go/listers,headerFile=./hack/boilerplate/boilerplate.go.txt" \
  "informer:clientsetName=kubernetes,externalOnly=true,standalone=true,outputPackagePath=github.com/kcp-dev/client-go,apiPackagePath=k8s.io/api,singleClusterClientPackagePath=k8s.io/client-go/kubernetes, singleClusterListerPackagePath=k8s.io/client-go/listers,singleClusterInformerPackagePath=k8s.io/client-go/informers,headerFile=./hack/boilerplate/boilerplate.go.txt" \
  "paths=$( go list -m -json k8s.io/api | jq --raw-output .Dir )/..." \
  "output:dir=./"

${CODE_GENERATOR} \
  "client:externalOnly=true,standalone=true,outputPackagePath=github.com/kcp-dev/client-go/apiextensions,name=client,apiPackagePath=k8s.io/apiextensions-apiserver/pkg/apis,singleClusterClientPackagePath=k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset,singleClusterApplyConfigurationsPackagePath=k8s.io/apiextensions-apiserver/pkg/client/applyconfiguration,headerFile=./hack/boilerplate/boilerplate.go.txt" \
  "lister:apiPackagePath=k8s.io/apiextensions-apiserver/pkg/apis,singleClusterListerPackagePath=k8s.io/apiextensions-apiserver/pkg/client/listers,headerFile=./hack/boilerplate/boilerplate.go.txt" \
  "informer:clientsetName=client,externalOnly=true,standalone=true,outputPackagePath=github.com/kcp-dev/client-go/apiextensions,apiPackagePath=k8s.io/apiextensions-apiserver/pkg/apis,singleClusterClientPackagePath=k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset, singleClusterListerPackagePath=k8s.io/apiextensions-apiserver/pkg/client/listers,singleClusterInformerPackagePath=k8s.io/apiextensions-apiserver/pkg/client/informers/externalversions,headerFile=./hack/boilerplate/boilerplate.go.txt" \
  "paths=$( go list -m -json k8s.io/apiextensions-apiserver | jq --raw-output .Dir )/pkg/apis/..." \
  "output:dir=./apiextensions"
