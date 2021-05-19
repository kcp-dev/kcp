#!/bin/bash

DEMO_ROOT="$(dirname "${BASH_SOURCE}")/../.."

rm -f ${DEMO_ROOT}/clusters/kind/us-east1-kubeconfig
kind create cluster --config ${DEMO_ROOT}/clusters/kind/us-east1.config --kubeconfig ${DEMO_ROOT}/clusters/kind/us-east1.kubeconfig
sed -e 's/^/    /' ${DEMO_ROOT}/clusters/kind/us-east1.kubeconfig | cat ${DEMO_ROOT}/clusters/us-east1.yaml - > ${DEMO_ROOT}/clusters/kind/us-east1.yaml

rm -f ${DEMO_ROOT}/clusters/kind/us-west1-kubeconfig
kind create cluster --config ${DEMO_ROOT}/clusters/kind/us-west1.config --kubeconfig ${DEMO_ROOT}/clusters/kind/us-west1.kubeconfig
sed -e 's/^/    /' ${DEMO_ROOT}/clusters/kind/us-west1.kubeconfig | cat ${DEMO_ROOT}/clusters/us-west1.yaml - > ${DEMO_ROOT}/clusters/kind/us-west1.yaml
