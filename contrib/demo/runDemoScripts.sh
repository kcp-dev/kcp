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

DEMOS_DIR="$(cd $(dirname "${BASH_SOURCE}") && pwd)"
KCP_DIR="$(cd ${DEMOS_DIR}/../.. && pwd)"

CURRENT_DIR="$(pwd)"

# For podman 3.x - workaround for Mac connecting to kind clusters.
# This is a relative path because ssh doesn't like long -S arg values
export PODMAN_SSH_CONTROL_SOCKET="clusters/kind/podman-ssh-control.sock"

DEMO_AS_TESTS=false
while getopts ":t" opt; do
  case ${opt} in
    t ) DEMO_AS_TESTS=true
      ;;
    \? ) echo "Usage: $(basename $0) [-t] <demo names>..."
         echo "  If no demo name is provided, all the demos are played"
         echo "  Use the -t option to run the demos in non-interactive mode, mainly as tests"
         exit 0
      ;;
  esac
done
shift $((OPTIND -1))
if [[ $# -eq 0 ]]; then
  DEMOS="apiNegotiation kubecon"
else
  DEMOS=$*
fi

source ${DEMOS_DIR}/.startUtils
setupTraps $0

cleanupDemoOutput() {
  sed -E -e 's/\x1b\[(3J|H|2J)//g' -e '/^☸️ \$ /d' -e '/^ +certificate-authority-data: /d' -e '/^ +server: https:\/\/127\.0\.0\.1:[0-9]{5}$/d' -e 's/[0-9]+(s|m)/10s/g' -e 's/^(.*)( +[0-9]+s|[0-9]+m([0-9]+s)?)$/\1/' $1
}

export TERM=xterm-256color

cols=100
if command -v tput &> /dev/null; then
  output=$(echo -e cols | tput -S)
  if [[ -n "${output}" ]]; then
    cols=$((output - 10))
  fi
fi
export cols

error=false
for demo in ${DEMOS} ; do
  demoDirName="$demo"-test
  rm -Rf "$demoDirName"
  mkdir "$demoDirName"
  TEST_DIR="$(cd $demoDirName && pwd)"
  cd "${TEST_DIR}"

  DEMO_DIR=${DEMOS_DIR}/${demo}-script

  if test -f "${DEMO_DIR}/prepareClusters.sh" ; then
    echo "Preparing Kind physical clusters for demo ${demo} ..."
    if ! ${DEMO_DIR}/prepareClusters.sh &> ${TEST_DIR}/clusters.log; then
      echo "Error starting Kind clusters - check ${TEST_DIR}/clusters.log for details"
      exit 1
    fi
  fi

  echo "Starting demo ${demo} in directory ${TEST_DIR}"
  export KCP_DATA_DIR="${TEST_DIR}"
  export KUBECONFIG=${KCP_DATA_DIR}/.kcp/admin.kubeconfig

  echo "Starting KCP for demo..."
  ${DEMO_DIR}/startKcp.sh &> "${TEST_DIR}"/start-kcp.log &
  TEST_KCP_PID=$!

  echo "Waiting for KCP to be started..."
  wait_command "test -f ${KCP_DATA_DIR}/servers-ready"

  echo "Running demo ${demo} and output logs to ${TEST_DIR}/demo.log..."
  if $DEMO_AS_TESTS ; then
    (export NO_COLORS="true" ; ${DEMO_DIR}/script -n &> demo.log)
  else
    (${DEMO_DIR}/script 2>&1 tee demo.log)
  fi

  echo "Dumping the admin logical cluster APIs for demo ${demo} to ${TEST_DIR}/apis.log..."
  kubectl get apiresourceimports -o wide >>${TEST_DIR}/apis.log 2>&1
  kubectl get negotiatedapiresources -o wide >>${TEST_DIR}/apis.log 2>&1

  echo "Stopping KCP and cleaning physical clusters for demo ${demo}"
  kill $TEST_KCP_PID

  if test -f "${DEMO_DIR}/removeClusters.sh" ; then
    ${DEMO_DIR}/removeClusters.sh
  fi
  cd ${CURRENT_DIR}

  if ! diff <(cleanupDemoOutput ${DEMO_DIR}/result.txt) <(cleanupDemoOutput ${TEST_DIR}/demo.log) > ${TEST_DIR}/diff.log ; then
    echo "Demo $demo failed ! Difference between expected and effective outputs is in the ${TEST_DIR}/diff.log file"
    error=true
  else
    echo "Demo $demo succeeded !"
  fi
  echo
done

if $error ; then
  exit 1
fi
