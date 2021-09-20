#!/bin/bash

# assumption this script is run from make target `make e2e`
# there may be relative path problem

trap cleanup 1 2 3 6

cleanup() {
  echo "Killing KCP and the KCP-OCM controllers"
  kill $KCP_PID
}

#clear KCP data 
rm -rf .kcp

#start kcp
./bin/kcp start &> kcp.log &
KCP_PID=${!}
echo "KCP server started: $KCP_PID" 

#validate kcp is running
kill -0 ${KCP_PID}
if [[ "$?" != 0 ]]; then
    echo "KCP not running check the kcp.log"
    exit 1
fi

export KUBECONFIG=./.kcp/data/admin.kubeconfig

echo -ne "Waiting for KCP to come up"
i=0
while [ $i -lt 5 ]; do
  echo -ne "."
  kubectl get namespace &> /dev/null
  if [ "$?" = "0" ]; then
    echo ""
    break
  fi
  i=$((i+1))
  sleep 1
done

if [ $i -eq 5 ]; then
    echo ""
    echo "KCP not running check the kcp.log"
    exit 1
fi

#hackaround to pass KUTTL initial check
kubectl create namespace default
kubectl create sa default

#start test
kubectl kuttl test

#cleanup
kill $KCP_PID



