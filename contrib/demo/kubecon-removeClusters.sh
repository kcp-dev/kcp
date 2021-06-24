#!/bin/bash

DEMO_ROOT="$(dirname "${BASH_SOURCE}")"
CLUSTERS_DIR=${DEMO_ROOT}/clusters/kind

kubectl --kubeconfig=${CLUSTERS_DIR}/us-west1.kubeconfig get deployment my-deployment--us-west1 -o yaml -n demo >>us-west1.log 2>&1
kubectl --kubeconfig=${CLUSTERS_DIR}/us-west1.kubeconfig get pods -o wide -n demo >>us-west1.log 2>&1
kubectl --kubeconfig=${CLUSTERS_DIR}/us-east1.kubeconfig get deployment my-deployment--us-east1 -o yaml -n demo >>us-east1.log 2>&1
kubectl --kubeconfig=${CLUSTERS_DIR}/us-east1.kubeconfig get pods -o wide -n demo >>us-east1.log 2>&1

kind delete clusters us-west1 us-west1 us-east1 > /dev/null || true
