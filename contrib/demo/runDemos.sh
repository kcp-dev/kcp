#!/bin/bash

CURRENT_DIR="$(pwd)"
DEMO_ROOT="$(cd $(dirname "${BASH_SOURCE}") && pwd)"
KCP_ROOT="$(cd ${DEMO_ROOT}/../.. && pwd)"

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

source ${DEMO_ROOT}/.startUtils
setupTraps $0

cleanupDemoOutput() {
  sed -E --posix -e 's/\x1b\[(3J|H|2J)//g' -e '/^☸️ \$ /d' -e '/^ +certificate-authority-data: /d' -e '/^ +server: https:\/\/127\.0\.0\.1:[0-9]{5}$/d' -e 's/^(.*)( +[0-9]+s|[0-9]+m([0-9]+s)?)$/\1/' $1
}

export TERM=xterm-256color

error=false
for demo in ${DEMOS} ; do
  demoDirName="$demo"-test
  rm -Rf "$demoDirName"
  mkdir "$demoDirName"
  TEST_DIR="$(cd $demoDirName && pwd)"
  cd "${TEST_DIR}"
  
  echo "Preparing Kind physical clusters for demo ${demo} ..."  
  ${DEMO_ROOT}/${demo}-prepareClusters.sh &> ${TEST_DIR}/clusters.log

  echo "Starting demo ${demo} in directory ${TEST_DIR}"
  export KCP_DATA_ROOT="${TEST_DIR}"
  export KUBECONFIG=${KCP_DATA_ROOT}/.kcp/admin.kubeconfig
  
  echo "Starting KCP for demo..."
  ${DEMO_ROOT}/${demo}-startKcp.sh &> /dev/null &
  TEST_KCP_PID=$!
  
  echo "Waiting for KCP to be started..."
  wait_command "grep 'Serving securely' ${TEST_DIR}/kcp.log" 60

  echo "Running demo ${demo} and output logs to ${TEST_DIR}/demo.log..."
  if $DEMO_AS_TESTS ; then
    (export NO_COLORS="true" ; ${DEMO_ROOT}/$demo -n &> demo.log)
  else
    (${DEMO_ROOT}/$demo |& tee demo.log)
  fi

  echo "Dumping the admin logical cluster APIs for demo ${demo} to ${TEST_DIR}/apis.log..."
  kubectl get apiresourceimports -o wide >>${TEST_DIR}/apis.log 2>&1
  kubectl get negotiatedapiresources -o wide >>${TEST_DIR}/apis.log 2>&1

  echo "Stopping KCP and cleaning physical clusters for demo ${demo}"
  kill $TEST_KCP_PID
  ${DEMO_ROOT}/${demo}-removeClusters.sh
  cd ${CURRENT_DIR}

  if ! diff <(cleanupDemoOutput ${DEMO_ROOT}/${demo}.result) <(cleanupDemoOutput ${TEST_DIR}/demo.log) > ${TEST_DIR}/diff.log ; then
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
