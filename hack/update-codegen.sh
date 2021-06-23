#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

export GOPATH=$(go env GOPATH)
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
    go get sigs.k8s.io/controller-tools/cmd/controller-gen
    go install sigs.k8s.io/controller-tools/cmd/controller-gen
fi

SCRIPT_ROOT=$(dirname "${BASH_SOURCE[0]}")/..
CODEGEN_PKG=${CODEGEN_PKG:-$(cd "${SCRIPT_ROOT}"; go list -f '{{.Dir}}' -m k8s.io/code-generator)}

bash "${CODEGEN_PKG}"/generate-groups.sh "deepcopy,client,informer,lister" \
  github.com/kcp-dev/kcp/pkg/client github.com/kcp-dev/kcp/pkg/apis \
  "cluster:v1alpha1 apiresource:v1alpha1" \
  --go-header-file "${SCRIPT_ROOT}"/hack/boilerplate.go.txt --output-base ${GOPATH}/src

# Update generated CRD YAML
${GOPATH}/bin/controller-gen crd:preserveUnknownFields=false rbac:roleName=manager-role webhook paths="./..." output:crd:artifacts:config=config/
