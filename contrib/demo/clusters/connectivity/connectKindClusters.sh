#!/bin/bash -e

DEMO_ROOT="$(dirname "${BASH_SOURCE}")/../.."
KUBECONFIGS_ROOT="${DEMO_ROOT}/clusters"
CLUSTER_KUBECONFIGS=(${@:-$(ls ${KUBECONFIGS_ROOT}/*.kubeconfig)})
CONNECTION_METHOD=${CONNECTION_METHOD:-submariner}

if [ -z "${CLUSTER_KUBECONFIGS[@]}" ]; then
  echo "no kubeconfig .yaml files found in ${KUBECONFIGS_ROOT}" 1>&2
  exit 1
fi

install_submariner() {
  curl https://get.submariner.io | bash
  export PATH=$PATH:~/.local/bin

  # first, deploy a broker, this could well go on the KCP API, as it's a pull-mode
  # API where all gateways exchange information
  subctl deploy-broker --kubeconfig ${CLUSTER_KUBECONFIGS[0]} --globalnet

  # join all clusters to the broker
  for config in "${CLUSTER_KUBECONFIGS[@]}"; do
    subctl join --kubeconfig $config broker-info.subm
  done
}

case $CONNECTION_METHOD in
  submariner) install_submariner
  ;;

  *)
    echo "unknown CONNECTION_METHOD ${CONNECTION_METHOD}" 1>&2
    exit 1
esac

