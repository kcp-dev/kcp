#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

export GOPATH=$(go env GOPATH)
if [[ -x "${GOPATH}/bin/controller-gen" ]]
then
    if [[ $( "${GOPATH}/bin/controller-gen" --version) != "Version: v0.5.0" ]]
    then
        echo "You must use at version 0.5.0 of controller-gen"
        exit 1
    fi
else
    echo "Installing 'controller-gen'"
    go get sigs.k8s.io/controller-tools/cmd/controller-gen@v0.5.0
    go install sigs.k8s.io/controller-tools/cmd/controller-gen@v0.5.0
fi

# Update generated CRD YAML
${GOPATH}/bin/controller-gen crd:preserveUnknownFields=false rbac:roleName=manager-role webhook paths="./..." output:crd:artifacts:config=config/
