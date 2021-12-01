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

set -o nounset
set -o pipefail
set -o errexit

KCP_ROOT="$( realpath "$(dirname "${BASH_SOURCE[0]}")/.." )"

workdir="${WORKDIR:-"$( mktemp -d )"}"
rm -rf "${workdir}"
mkdir -p "${WORKDIR}"
echo "Workdir: ${workdir}"
pushd "${workdir}"

function cleanup() {
  for job in $( jobs -p ); do
    kill "${job}"
  done
  wait
}
trap cleanup EXIT

echo "Starting clusters..."
"${KCP_ROOT}"/bin/kcp start --push_mode --install_cluster_controller --auto_publish_apis --resources_to_sync=cowboys.wildwest.dev --root_directory "${workdir}/.kcp" > "${workdir}/.kcp.log" 2>&1 &
export kcp="${workdir}/.kcp/admin.kubeconfig"
"${KCP_ROOT}"/bin/kcp start --root_directory "${workdir}/.kcp2" --etcd_client_port 2381 --etcd_peer_port 2382 --listen :6444 > "${workdir}/.kcp2.log" 2>&1 &
export pcluster="${workdir}/.kcp2/admin.kubeconfig"
for cluster in kcp kcp2; do
  success="false"
  for i in {1..20}; do
      if [[ -s "${pcluster}" ]]; then
        if [[ "$( kubectl --kubeconfig "${workdir}/.${cluster}/admin.kubeconfig" get --raw /readyz 2>&1 )" == "ok" ]] ; then
          success="true"
          break
        fi
      fi
      echo -n .
      sleep 1
      continue
  done
  echo
  if [[ "${success}" != "true" ]]; then
    echo "ERROR: could not start kcp, logs:"
    cat "${workdir}/.${cluster}.log"
    exit 1
  fi
done

echo "Creating CRD..."
cat >"${workdir}/cowboy.yaml" <<EOF
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: cowboys.wildwest.dev
spec:
  group: wildwest.dev
  names:
    categories:
    - test
    kind: Cowboy
    listKind: CowboyList
    plural: cowboys
    singular: cowboy
  scope: Namespaced
  versions:
  - name: v1alpha1
    schema:
      openAPIV3Schema:
        properties:
          apiVersion:
            description: 'APIVersion defines the versioned schema of this representation
              of an object. Servers should convert recognized schemas to the latest
              internal value, and may reject unrecognized values. More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources'
            type: string
          kind:
            description: 'Kind is a string value representing the REST resource this
              object represents. Servers may infer this from the endpoint the client
              submits requests to. Cannot be updated. In CamelCase. More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds'
            type: string
          metadata:
            type: object
          spec:
            description: Spec holds the desired state.
            properties:
              intent:
                type: string
            required:
            - intent
            type: object
          status:
            description: Status communicates the observed state.
            properties:
              result:
                type: string
            type: object
        type: object
    served: true
    storage: true
    subresources:
      status: {}
status:
  acceptedNames:
    kind: ""
    plural: ""
  conditions: []
  storedVersions: []
EOF
for cluster in kcp kcp2; do
  kubectl --kubeconfig "${workdir}/.${cluster}/admin.kubeconfig" apply -f "${workdir}/cowboy.yaml"
done
for cluster in kcp kcp2; do
  kubectl --kubeconfig "${workdir}/.${cluster}/admin.kubeconfig" wait customresourcedefinition/cowboys.wildwest.dev --for condition=Established=true
done

cat >"${workdir}/us-east1.raw" <<EOF
kind: Cluster
apiVersion: cluster.example.dev/v1alpha1
metadata:
  name: us-east1
spec:
  kubeconfig: |
EOF

sed -e 's/^/    /' "${pcluster}" | cat "${workdir}/us-east1.raw" - > "${workdir}/us-east1.yaml"
echo "Adding pcluster as a Cluster to kcp..."
kubectl --kubeconfig "${kcp}" apply -f "${workdir}/us-east1.yaml"
kubectl --kubeconfig "${kcp}" wait cluster.cluster.example.dev/us-east1 --for condition=Ready=true

cat >"${workdir}/timothy.yaml" <<EOF
apiVersion: wildwest.dev/v1alpha1
kind: Cowboy
metadata:
  name: timothy
  labels:
    kcp.dev/cluster: us-east1
spec:
  intent: yeehaw
EOF

echo "Creating cowboy on kcp..."
kubectl --kubeconfig "${kcp}" --context admin create namespace test
kubectl --kubeconfig "${kcp}" apply -f "${workdir}/timothy.yaml" --namespace test
echo "Waiting for cowboy spec to sync to pcluster..."
spec_success="false"
for i in {1..20} ; do
  if kubectl --kubeconfig "${pcluster}" get namespace test >/dev/null 2>&1; then
      if kubectl --kubeconfig "${pcluster}" --namespace test get cowboy timothy >/dev/null 2>&1; then
          if [[ "$( kubectl --kubeconfig "${pcluster}" --namespace test get cowboy timothy -o jsonpath={.spec.intent} 2>&1 )" == "yeehaw" ]]; then
            spec_success="true"
            break
          fi
      fi
  fi
  echo -n .
  sleep 1
done
echo
if [[ "${spec_success}" != "true" ]]; then
  echo "ERROR: did not sync spec to pcluster:"
  echo "ERROR: state on kcp:"
  kubectl --kubeconfig "${kcp}" --namespace test get cowboy timothy -o yaml
  echo "ERROR: state on pcluster:"
  kubectl --kubeconfig "${pcluster}" --namespace test get cowboy timothy -o yaml
  exit 1
fi

echo "Changing status on the pcluster..."
# don't make everyone install yq...
extract_token=$(cat <<EOF
import yaml
import sys
with open(sys.argv[1]) as raw:
  data = yaml.load(raw, Loader=yaml.FullLoader)
  print(data['users'][0]['user']['token'])
EOF
)
extract_server=$(cat <<EOF
import yaml
import sys
with open(sys.argv[1]) as raw:
  data = yaml.load(raw, Loader=yaml.FullLoader)
  print(data['clusters'][0]['cluster']['server'])
EOF
)
curl -k -XPATCH -H "Content-Type: application/merge-patch+json" \
                -H "Accept: application/json" \
                -H "Authorization: Bearer $( python -c "${extract_token}" "${pcluster}" )" \
                --data '{"status":{"result":"giddyup"}}' \
                "$( python -c "${extract_server}" "${pcluster}" )/apis/wildwest.dev/v1alpha1/namespaces/test/cowboys/timothy/status"
echo "Waiting for cowboy status to sync to kcp..."
status_success="false"
for i in {1..20} ; do
  if kubectl --kubeconfig "${kcp}" get namespace test >/dev/null 2>&1; then
      if kubectl --kubeconfig "${kcp}" --namespace test get cowboy timothy >/dev/null 2>&1; then
          if [[ "$( kubectl --kubeconfig "${kcp}" --namespace test get cowboy timothy -o jsonpath={.status.result} 2>&1 )" == "giddyup" ]]; then
            status_success="true"
            break
          fi
      fi
  fi
  echo -n .
  sleep 1
done
echo
if [[ "${status_success}" != "true" ]]; then
  echo "ERROR: did not sync status to kcp:"
  echo "ERROR: state on kcp:"
  kubectl --kubeconfig "${kcp}" --namespace test get cowboy timothy -o yaml
  echo "ERROR: state on pcluster:"
  kubectl --kubeconfig "${pcluster}" --namespace test get cowboy timothy -o yaml
  exit 1
fi

echo "Cluster and syncer test done"