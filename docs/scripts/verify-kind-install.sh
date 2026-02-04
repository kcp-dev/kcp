#!/bin/bash

# verification script for step 6-10 of quickstart-kind.md

set -o errexit
set -o nounset
set -o pipefail

KCP_NAMESPACE="kcp"
KCP_EXTERNAL_HOSTNAME=${KCP_EXTERNAL_HOSTNAME:-"kcp.local.test"}
KCP_PORT=${KCP_PORT:-8443}

if [ -z "${KUBECONFIG:-}" ]; then
    if [ -f admin.kubeconfig ]; then
        export KUBECONFIG=admin.kubeconfig
    else
        echo "KUBECONFIG is not set and admin.kubeconfig is missing. Run Step 5 first."
        exit 1
    fi
fi

echo "Starting verification..."

# Step 6: Create Team Workspaces
echo "=== Step 6: Creating team workspaces ==="
for team in team-alpha team-beta team-gamma team-delta; do
    if kubectl ws :root:"${team}" > /dev/null 2>&1; then
        echo "Workspace ${team} already exists, skipping..."
    else
        kubectl ws create "${team}" --enter
        kubectl ws ..
    fi
done
kubectl ws :root

echo "Workspaces created:"
kubectl ws tree

# Step 7: Generate Team Certificates
echo "=== Step 7: Generating team certificates ==="
for team in alpha beta gamma delta; do
    cat <<EOF | kubectl apply -n ${KCP_NAMESPACE} -f -
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: team-${team}-cert
spec:
  commonName: team-${team}-admin
  issuerRef:
    name: kcp-front-proxy-client-issuer
    kind: Issuer
  secretName: team-${team}-cert
  privateKey:
    algorithm: RSA
    size: 2048
  usages:
    - client auth
  subject:
    organizations:
      - team-${team}
EOF
done

echo "Waiting for certificates to be ready..."
for team in alpha beta gamma delta; do
    kubectl wait --for=condition=Ready certificate/team-${team}-cert -n ${KCP_NAMESPACE} --timeout=60s
done

# Step 8: Grant Workspace Access
echo "=== Step 8: Granting workspace access ==="

for team in alpha beta gamma delta; do
    echo "Configuring access for team-${team}..."
    kubectl ws :root:team-${team}

    cat <<EOF | kubectl apply -f -
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: team-${team}-admin
subjects:
- kind: Group
  name: team-${team}
  apiGroup: rbac.authorization.k8s.io
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: cluster-admin
EOF
done

kubectl ws :root

# Step 9: Create Team Kubeconfigs
echo "=== Step 9: Creating team kubeconfigs ==="

# Ensure CA cert is available
if [ ! -f ca.crt ]; then
    echo "Extracting CA certificate..."
    kubectl get secret kcp-ca -n ${KCP_NAMESPACE} \
      -o=jsonpath='{.data.tls\.crt}' | base64 -d > ca.crt
fi

for team in alpha beta gamma delta; do
    echo "Processing team-${team}..."
    kubectl get secret team-${team}-cert -n ${KCP_NAMESPACE} \
        -o=jsonpath='{.data.tls\.crt}' | base64 -d > team-${team}.crt
    kubectl get secret team-${team}-cert -n ${KCP_NAMESPACE} \
        -o=jsonpath='{.data.tls\.key}' | base64 -d > team-${team}.key

    kubectl --kubeconfig=team-${team}.kubeconfig config set-cluster kcp \
        --server https://${KCP_EXTERNAL_HOSTNAME}:${KCP_PORT}/clusters/root:team-${team} \
        --certificate-authority=ca.crt

    kubectl --kubeconfig=team-${team}.kubeconfig config set-credentials team-${team} \
        --client-certificate=team-${team}.crt \
        --client-key=team-${team}.key

    kubectl --kubeconfig=team-${team}.kubeconfig config set-context team-${team} \
        --cluster=kcp \
        --user=team-${team}

    kubectl --kubeconfig=team-${team}.kubeconfig config use-context team-${team}
done

# Step 10: Verify Team Access
echo "=== Step 10: Verifying team access ==="
FAILED=0
for team in alpha beta gamma delta; do
    echo "--- team-${team} ---"
    if KUBECONFIG=team-${team}.kubeconfig kubectl get namespaces; then
        echo "Team ${team}: Access granted (SUCCESS)"
    else
        echo "Team ${team}: Access FAILED"
        FAILED=1
    fi
done

# Verify workspace isolation
echo "=== Verifying workspace isolation ==="
if KUBECONFIG=team-alpha.kubeconfig kubectl get namespaces \
    --server "https://${KCP_EXTERNAL_HOSTNAME}:${KCP_PORT}/clusters/root:team-beta" 2>/dev/null; then
    echo "ERROR: Team Alpha can access Team Beta workspace (isolation broken)"
    FAILED=1
else
    echo "OK: Team Alpha cannot access Team Beta workspace (isolation works)"
fi

if [ $FAILED -eq 0 ]; then
    echo ""
    echo "All verification checks passed!"
else
    echo ""
    echo "Some verification checks failed."
    exit 1
fi
