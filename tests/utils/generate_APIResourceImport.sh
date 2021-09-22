#!/bin/bash

# a hacky script to start different version of kind cluster and extract "specific" APIResourceImport from the kind cluster
# preqreq
# - kcp should be running 
# - cluster should be running 


declare -a versions=(
    "v1.20.7" 
    "v1.19.11" 
    "v1.18.19"
    "v1.17.17"
    "v1.16.15"
    "v1.15.12"
    "v1.14.10"
    "v1.13.12"
    "v1.12.10"
)

#assumption kubeconfig points to kcp

for version in ${versions[@]}; do
    kind create cluster --name $version --kubeconfig=/tmp/kind --image=kindest/node:$version
    cat <<EOF | kubectl apply -f -
apiVersion: cluster.example.dev/v1alpha1
kind: Cluster
metadata:
  name: kube-$version
spec:
  kubeconfig: |
`kubectl config view --minify=true --raw=true --context=kind-$version --kubeconfig=/tmp/kind | sed 's/^/    /'`
EOF
    sleep 10 #TODO: smarter wait
    kind delete cluster --name $version
done

#dump all apiresourceimport
for name in `kubectl get apiresourceimports -o custom-columns=NAME:.metadata.name --no-headers | grep kube`; do 
    kubectl get apiresourceimport $name -o yaml | kubectl neat > $name.yaml
done