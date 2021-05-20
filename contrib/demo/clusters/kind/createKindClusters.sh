#!/bin/bash -e

DEMO_ROOT="$(dirname "${BASH_SOURCE}")/../.."

kind create cluster --config ${DEMO_ROOT}/clusters/kind/us-east1.config --kubeconfig ${DEMO_ROOT}/clusters/kind/us-east1.kubeconfig
sed -e 's/^/    /' ${DEMO_ROOT}/clusters/kind/us-east1.kubeconfig | cat ${DEMO_ROOT}/clusters/us-east1.yaml - > ${DEMO_ROOT}/clusters/kind/us-east1.yaml

kind create cluster --config ${DEMO_ROOT}/clusters/kind/us-west1.config --kubeconfig ${DEMO_ROOT}/clusters/kind/us-west1.kubeconfig
sed -e 's/^/    /' ${DEMO_ROOT}/clusters/kind/us-west1.kubeconfig | cat ${DEMO_ROOT}/clusters/us-west1.yaml - > ${DEMO_ROOT}/clusters/kind/us-west1.yaml
