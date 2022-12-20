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

c "First we take the role of an API service provider. Our running example is cert-manager. We want to offer that to users of the kcp service."

c "Install cert-manager into one of the kind clusters"
pe "kubectl --kubeconfig ${CLUSTERS_DIR}/us-east1.kubeconfig apply -f ${DEMO_DIR}/apibinding/cert-manager.yaml"
kubectl --kubeconfig ${CLUSTERS_DIR}/us-east1.kubeconfig -n cert-manager delete configmap kubeconfig &>/dev/null
pe "kubectl config use-context system:admin"
pe "kubectl --kubeconfig ${CLUSTERS_DIR}/us-east1.kubeconfig -n cert-manager create configmap kubeconfig --from-file kubeconfig=$KUBECONFIG"
kubectl --kubeconfig ${CLUSTERS_DIR}/us-east1.kubeconfig -n cert-manager delete pods --all --grace-period=1

c "Then, we will need ingress support in our kind cluster:"
pe "kubectl --kubeconfig ${CLUSTERS_DIR}/us-east1.kubeconfig apply -f ${DEMOS_DIR}/ingress-script/nginx-ingress.yaml"

pe "kubectl config use-context root"
pe "kubectl kcp workspace use demo"

c "Then in kcp, we create a workspace for cert-manager to export the APIs from:"
pe "kubectl kcp workspace create cert-manager-service"
sleep 1
pe "kubectl kcp workspace use cert-manager-service"

c "Then we need an APIResourceSchema. That's very similar to a CRD, actually like a snapshot of a CRD at a point in time:"
pe "cat ${DEMO_DIR}/apibinding/certificates-apiresourceschema.yaml"
pe "kubectl apply -f ${DEMO_DIR}/apibinding/certificates-apiresourceschema.yaml"

c "Now we export the API to make it available for users of the kcp service:"
pe "cat ${DEMO_DIR}/apibinding/cert-manager-apiexport.yaml"
pe "kubectl apply -f ${DEMO_DIR}/apibinding/cert-manager-apiexport.yaml"
c "Note that it references our APIResourceSchema, more concretely today's version. In the future, we might want to have another version and roll that out to customers."

wait
clear

pe "kubectl config use-context root"
pe "kubectl kcp workspace use demo"

c "Now we switch roles. We are the customer now, in need of certificate generation for our web application. We are lucky: kcp has a cert-manager service 🎉"
pe "kubectl kcp workspace create webapp"
pe "kubectl kcp workspace use webapp"

c "We bind to the cert-manager API by creating an APIBinding:"
pe "cat ${DEMO_DIR}/apibinding/cert-manager-apibinding.yaml"
c "Note that we choose the stable version. There might be a cutting-edge APIExport too."
pe "kubectl apply -f ${DEMO_DIR}/apibinding/cert-manager-apibinding.yaml"

c "The kcp system will do the binding now in the background and make the APIs available:"
pe "kubectl get apibinding -o yaml"

sleep 2

pe "kubectl get apibinding -o yaml"
c "We see it is bound and ready. We should now see the Certificate API available 🤞:"
pe "kubectl api-resources"
pe "kubectl get certificates"

c "Note that this is much more than just a CRD: we have bound to the whole cert-manager service. So certificate requests will actually work 🎉"

c "Let's start by creating a private key for ca issuer. This step requires openssl to be installed locally"
pe "openssl req -nodes -x509 -newkey rsa:2048 \
  -subj \"/C=US/ST=New York/L=New York City/O=Kubernetes/OU=KCP/CN=example.com/emailAddress=test@example.com\" \
  -keyout ${KCP_DATA_DIR}/ca.key \
  -out ${KCP_DATA_DIR}/ca.crt \
  -extensions v3_ca -config ${DEMO_DIR}/apibinding/openssl.cnf"
kubectl delete issuers ca-issuer &>/dev/null
kubectl delete certificates serving-cert &>/dev/null
kubectl delete secret ca-key-pair ca-cert-tls &>/dev/null
pe "kubectl create secret tls ca-key-pair --cert=${KCP_DATA_DIR}/ca.crt --key=${KCP_DATA_DIR}/ca.key"

c "Let's request a serving cert for our web application:"

pe "cat ${DEMO_DIR}/apibinding/issuer.yaml"
pe "cat ${DEMO_DIR}/apibinding/certificate.yaml"

pe "kubectl apply -f ${DEMO_DIR}/apibinding/issuer.yaml"
pe "kubectl apply -f ${DEMO_DIR}/apibinding/certificate.yaml"
pe "kubectl get secrets"

c "Now let's deploy an app. First we need a target workload cluster."
cat <<EOF > "${KCP_DATA_DIR}/cluster-us-east1.yaml"
apiVersion: workload.kcp.io/v1alpha1
kind: SyncTarget
metadata:
  name: kind-us-east1
spec:
  kubeconfig: |
$(sed 's,^,    ,' "${CLUSTERS_DIR}"/us-east1.kubeconfig)
EOF
pe "kubectl apply -f ${KCP_DATA_DIR}/cluster-us-east1.yaml"

c "Let's wait for kcp to have the cluster syncing ready"
pe "kubectl wait --for condition=Ready workloadcluster/kind-us-east1"

wait
clear

# Use the right arch for the kuard image
kuardArch=$(uname -m)
if [[ "${kuardArch}" == "x86_64" ]]; then
  kuardArch=amd64
fi
sed "s/ARCH/$kuardArch/" "${DEMO_DIR}"/apibinding/deployment-kuard-tls.yaml > "${KCP_DATA_DIR}"/deployment-kuard-tls.yaml
pe "cat ${KCP_DATA_DIR}/deployment-kuard-tls.yaml"
pe "kubectl apply -f ${KCP_DATA_DIR}/deployment-kuard-tls.yaml"

c "Take note of Replicas:"
pe "kubectl describe deployment/kuard"

c "Let's wait for the deployment to be running in kind"
pe "while ! kubectl --kubeconfig ${CLUSTERS_DIR}/us-east1.kubeconfig wait --for=jsonpath='{.status.availableReplicas}'=1 -A -l 'workloads.kcp.io/cluster=kind-us-east1' --field-selector 'metadata.name=kuard' deployments; do
  sleep 1
done"

c "Let's describe it to see its status - take note of Replicas"
pe "kubectl describe deployment/kuard"

c "Does it actually serve with cert-manager-produced certs? We will find out."
c "First a service to put in front, and an ingress to route to it"
pe "cat ${DEMO_DIR}/apibinding/kuard-service.yaml"
pe "cat ${DEMO_DIR}/apibinding/kuard-ingress.yaml"
pe "kubectl apply -f ${DEMO_DIR}/apibinding/kuard-service.yaml"
pe "kubectl apply -f ${DEMO_DIR}/apibinding/kuard-ingress.yaml"
sleep 3
pe "kubectl describe ingress kuard"
c "We can now access the service with curl:"
pe "curl -v --cacert ${KCP_DATA_DIR}/ca.crt --resolve kuard.kcp-apps.127.0.0.1.nip.io:8443:127.0.0.1 https://kuard.kcp-apps.127.0.0.1.nip.io:8443"
c "BINGO! 🎉"
