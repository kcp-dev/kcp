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

if [[ "$1" == "" ]]
then
    echo "Usage: generate-crds.sh <output directory>"
    exit 1
fi
destination="$(realpath $1)"

echo "Checking the presence of 'controller-gen'"

GOPATH=$(go env GOPATH)
if [[ -x "${GOPATH}/bin/controller-gen" ]]
then
    version=$(${GOPATH}/bin/controller-gen --version | sed -e 's/Version: v0\.\(5\)\../\1/')
    if [[ $version -lt 5 ]]
    then
        echo "You should use at least version 0.5.0 of controller-gen" 
        exit 1
    fi
else
    echo "Installing 'controller-gen'"
    go install sigs.k8s.io/controller-tools/cmd/controller-gen@latest
fi

mkdir crd-build
pushd crd-build > /dev/null

echo "Cloning Kubernetes 'crd-compatible-core-and-apps-types' branch into $(pwd)"
git clone --depth 1 --branch crd-compatible-core-and-apps-types git@github.com:kcp-dev/kubernetes.git

pushd kubernetes/staging/src/k8s.io/api > /dev/null

echo "Generating core/v1 CRDs"
${GOPATH}/bin/controller-gen crd:crdVersions=v1 paths=./core/v1 output:crd:dir=${destination}/core output:stdout

echo "Removing unnecessary core/v1 resources"
rm $(ls ${destination}/core/*.yaml | grep -v -E '.*(pods|nodes|endpoints|persistentvolumeclaims|persistentvolumes|podtemplates|replicationcontrollers|services)\.yaml')

echo "Adding the 'core' group as a suffix in the name of core/v1 CRDs "
sed -i -e 's/^\(  name: [^.]*\)\.$/\1.core/' ${destination}/core/*.yaml

echo "Generating apps/v1 CRDs"
${GOPATH}/bin/controller-gen crd:crdVersions=v1 paths=./apps/v1 output:crd:dir=${destination}/apps output:stdout

popd > /dev/null
popd > /dev/null

rm -Rf crd-build
