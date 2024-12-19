#!/bin/bash

set -eox pipefail

while true; do
    kubectl ws use :root:consumer
    clusters=$(kubectl ws tree | tail -n +3 | awk '{print $NF}' | grep -v "^$" | grep -v consumer || true;)

    for cluster in $clusters; do
        if ! kind get clusters | grep -q $cluster; then
            kind create cluster --name $cluster --kubeconfig $cluster.kubeconfig

            kubectl ws use :root:operators:mounts

            kubectl delete secret $cluster-kubeconfig || true
            kubectl create secret generic $cluster-kubeconfig --from-file=kubeconfig=$cluster.kubeconfig

            cat <<EOF | kubectl apply -f -
apiVersion: targets.contrib.kcp.io/v1alpha1
kind: TargetKubeCluster
metadata:
  name: $cluster
spec:
  secretRef:
    name: $cluster-kubeconfig
    namespace: default
EOF

            secret="$(kubectl get TargetKubeCluster $cluster -o jsonpath='{.status.secretString}')"
            while [ -z "$(kubectl get TargetKubeCluster $cluster -o jsonpath='{.status.secretString}')" ]; do
                sleep 1
            done

            kubectl ws use :root:consumer

            cat <<EOF | kubectl apply -f -
apiVersion: mounts.contrib.kcp.io/v1alpha1
kind: KubeCluster
metadata:
  name: $cluster
  annotations:
    experimental.tenancy.kcp.io/is-mount: "true"
spec:
  mode: Delegated
  secretString: "$secret"
EOF

            while [ -z "$(kubectl get KubeCluster $cluster)" ]; do
                sleep 1
            done

            kubectl annotate --overwrite workspace $cluster experimental.tenancy.kcp.io/mount='{"spec":{"ref":{"kind":"KubeCluster","name":"'$cluster'","apiVersion":"mounts.contrib.kcp.io/v1alpha1"}}}' || true
        fi
    done

    sleep 5
done
