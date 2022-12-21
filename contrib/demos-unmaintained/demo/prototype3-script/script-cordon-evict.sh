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

DEMO_DIR="$(dirname "${BASH_SOURCE[0]}")"
source "${DEMO_DIR}"/../.setupEnv
source "${DEMO_DIR}"/script-preamble.sh

c "First create a workspace for this demo."
pe "kubectl kcp workspace create cordon-evict-demo --use"

c "Let's register our kind cluster us-east1 with kcp, as unschedulable"
pe "kubectl apply -f ${KCP_DIR}/config/crds/workload.kcp.io_workloadclusters.yaml"
cat <<EOF > "${KCP_DATA_DIR}/cluster-us-east1.yaml"
apiVersion: workload.kcp.io/v1alpha1
kind: SyncTarget
metadata:
  name: kind-us-east1
spec:
  unschedulable: true # <-- unschedulable!
  kubeconfig: |
$(sed 's,^,    ,' "${CLUSTERS_DIR}"/us-east1.kubeconfig)
EOF
pe "cat ${KCP_DATA_DIR}/cluster-us-east1.yaml"

wait;clear
pe "kubectl apply -f ${KCP_DATA_DIR}/cluster-us-east1.yaml"
c "Let's wait for kcp to have the cluster syncing ready"
pe "kubectl wait --for condition=Ready workloadcluster/kind-us-east1"

c "Now we can create a deployment"
pe "kubectl apply -f ${KCP_DIR}/contrib/crds/apps/apps_deployments.yaml"
pe "kubectl create namespace default"
pe "cat ${DEMO_DIR}/deployment-kuard.yaml"
pe "kubectl apply -f ${DEMO_DIR}/deployment-kuard.yaml"

c "See the deployment is not scheduling, because the only cluster is marked as unschedulable."
pe "kubectl get deployment/kuard -ojsonpath='{.metadata.labels}{\"\\n\"}'"

c "Let's mark the cluster as schedulable"
pe "kubectl patch workloadcluster kind-us-east1 --type=json --patch='[{\"op\":\"replace\",\"path\":\"/spec/unschedulable\",\"value\":false}]'"

c "See the deployment is scheduled to the workload cluster."
pe "kubectl get deployment/kuard -ojsonpath='{.metadata.labels}{\"\\n\"}'"

c "Now let's evict workloads from the cluster immediately"
pe "kubectl patch workloadcluster kind-us-east1 --type=json --patch='[{\"op\":\"replace\",\"path\":\"/spec/evictAfter\",\"value\":\"2022-01-01T00:00:00+00:00\"}]'"

c "See the deployment is no longer scheduled, because the only workload cluster is evicted."
pe "kubectl get deployment/kuard -ojsonpath='{.metadata.labels}{\"\\n\"}'"
