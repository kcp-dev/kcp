---
description: >
  A comprehensive guide to deploying kcp on a kind cluster using Helm, with multi-tenant workspace setup.
---

# Quickstart with kind

This guide walks you through deploying kcp on a [kind](https://kind.sigs.k8s.io/) (Kubernetes in Docker) cluster using Helm, setting up multiple workspaces, and configuring access for different teams using client certificates.

This guide covers the Helm-based deployment. A kcp-operator-based walkthrough will be added in a follow-up.

## Prerequisites

Before starting, ensure you have the following tools installed:

- [Docker](https://docs.docker.com/get-docker/)
- [kind](https://kind.sigs.k8s.io/docs/user/quick-start/#installation)
- [kubectl](https://kubernetes.io/docs/tasks/tools/#kubectl)
- [Helm](https://helm.sh/docs/intro/install/) (v3.x)
- [kubectl kcp plugin](./kubectl-plugin.md)

## Step 1: Create a kind Cluster

Create a kind cluster with port mapping for kcp access:

```bash
cat <<EOF | kind create cluster --name kcp --config=-
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
  extraPortMappings:
  - containerPort: 30443
    hostPort: 8443
    protocol: TCP
EOF
```

Verify the cluster is running:

```bash
kubectl cluster-info --context kind-kcp
kubectl config use-context kind-kcp
```

## Step 2: Install cert-manager

kcp requires cert-manager for TLS certificate management:

```bash
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.19.2/cert-manager.yaml
```

Wait for cert-manager to be ready:

```bash
kubectl wait --for=condition=Available deployment --all -n cert-manager --timeout=300s
```

## Step 3: Configure DNS

For local development, add an entry to your hosts file:

```bash
echo "127.0.0.1 kcp.local.test" | sudo tee -a /etc/hosts
```

## Step 4: Deploy kcp with Helm

Add the kcp Helm repository:

```bash
helm repo add kcp https://kcp-dev.github.io/helm-charts
helm repo update
```

Install kcp:

```bash
helm upgrade --install kcp kcp/kcp \
  --namespace kcp \
  --create-namespace \
  --set externalHostname=kcp.local.test \
  --set externalPort=8443 \
  --set kcpFrontProxy.service.type=NodePort \
  --set kcpFrontProxy.service.nodePort=30443 \
  --set audit.enabled=false \
  --wait
```

Set up host aliases so in-cluster kcp components can resolve `kcp.local.test`:

```bash
KCP_FRONT_PROXY_IP=$(kubectl get svc kcp-front-proxy -n kcp -o jsonpath='{.spec.clusterIP}')
helm upgrade kcp kcp/kcp \
  --namespace kcp \
  --reuse-values \
  --set kcp.hostAliases.enabled=true \
  --set "kcp.hostAliases.values[0].ip=${KCP_FRONT_PROXY_IP}" \
  --set "kcp.hostAliases.values[0].hostnames[0]=kcp.local.test" \
  --set kcpFrontProxy.hostAliases.enabled=true \
  --set "kcpFrontProxy.hostAliases.values[0].ip=${KCP_FRONT_PROXY_IP}" \
  --set "kcpFrontProxy.hostAliases.values[0].hostnames[0]=kcp.local.test" \
  --wait
```

Verify the deployment:

```bash
kubectl get pods -n kcp
```

All pods should reach the `Running` state.

## Step 5: Configure Admin Access

Set environment variables for easier access:

```bash
export KCP_EXTERNAL_HOSTNAME=kcp.local.test
export KCP_PORT=8443
```

### Extract the CA Certificate

```bash
kubectl get secret kcp-ca -n kcp \
  -o=jsonpath='{.data.tls\.crt}' | base64 -d > ca.crt
```

### Create Admin Kubeconfig

```bash
kubectl --kubeconfig=admin.kubeconfig config set-cluster base \
  --server https://${KCP_EXTERNAL_HOSTNAME}:${KCP_PORT}/clusters/root \
  --certificate-authority=ca.crt
```

### Generate Admin Client Certificate

Create a certificate for the admin user:

```bash
kubectl apply -n kcp -f - <<EOF
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: cluster-admin-client-cert
spec:
  commonName: cluster-admin
  issuerRef:
    name: kcp-front-proxy-client-issuer
    kind: Issuer
  secretName: cluster-admin-client-cert
  privateKey:
    algorithm: RSA
    size: 2048
  usages:
    - client auth
  subject:
    organizations:
      - system:kcp:admin
EOF
```

Wait for the certificate:

```bash
kubectl wait --for=condition=Ready certificate/cluster-admin-client-cert -n kcp --timeout=60s
```

Extract the credentials:

```bash
kubectl get secret cluster-admin-client-cert -n kcp \
  -o=jsonpath='{.data.tls\.crt}' | base64 -d > admin-client.crt
kubectl get secret cluster-admin-client-cert -n kcp \
  -o=jsonpath='{.data.tls\.key}' | base64 -d > admin-client.key
```

Add credentials to kubeconfig:

```bash
kubectl --kubeconfig=admin.kubeconfig config set-credentials kcp-admin \
  --client-certificate=admin-client.crt \
  --client-key=admin-client.key

kubectl --kubeconfig=admin.kubeconfig config set-context base \
  --cluster=base \
  --user=kcp-admin

kubectl --kubeconfig=admin.kubeconfig config use-context base
```

Verify admin access:

```bash
export KUBECONFIG=admin.kubeconfig
kubectl ws tree
```

## Step 6: Create Team Workspaces

Create four workspaces for different teams:

```bash
kubectl ws create team-alpha --enter
kubectl ws ..
kubectl ws create team-beta --enter
kubectl ws ..
kubectl ws create team-gamma --enter
kubectl ws ..
kubectl ws create team-delta --enter
kubectl ws :root
```

> **Note:** Steps 6 through 10 can be automated using the verification script:
> ```bash
> ./docs/scripts/verify-kind-install.sh
> ```


Verify the workspaces:

```bash
kubectl ws tree
```

You should see:

```
root
├── team-alpha
├── team-beta
├── team-gamma
└── team-delta
```

## Step 7: Generate Team Certificates

Create client certificates for each team with their respective groups.

### Team Alpha

```bash
kubectl apply -n kcp -f - <<EOF
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: team-alpha-cert
spec:
  commonName: team-alpha-admin
  issuerRef:
    name: kcp-front-proxy-client-issuer
    kind: Issuer
  secretName: team-alpha-cert
  privateKey:
    algorithm: RSA
    size: 2048
  usages:
    - client auth
  subject:
    organizations:
      - team-alpha
EOF
```

### Team Beta

```bash
kubectl apply -n kcp -f - <<EOF
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: team-beta-cert
spec:
  commonName: team-beta-admin
  issuerRef:
    name: kcp-front-proxy-client-issuer
    kind: Issuer
  secretName: team-beta-cert
  privateKey:
    algorithm: RSA
    size: 2048
  usages:
    - client auth
  subject:
    organizations:
      - team-beta
EOF
```

### Team Gamma

```bash
kubectl apply -n kcp -f - <<EOF
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: team-gamma-cert
spec:
  commonName: team-gamma-admin
  issuerRef:
    name: kcp-front-proxy-client-issuer
    kind: Issuer
  secretName: team-gamma-cert
  privateKey:
    algorithm: RSA
    size: 2048
  usages:
    - client auth
  subject:
    organizations:
      - team-gamma
EOF
```

### Team Delta

```bash
kubectl apply -n kcp -f - <<EOF
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: team-delta-cert
spec:
  commonName: team-delta-admin
  issuerRef:
    name: kcp-front-proxy-client-issuer
    kind: Issuer
  secretName: team-delta-cert
  privateKey:
    algorithm: RSA
    size: 2048
  usages:
    - client auth
  subject:
    organizations:
      - team-delta
EOF
```

Wait for all certificates:

```bash
for team in alpha beta gamma delta; do
  kubectl wait --for=condition=Ready certificate/team-${team}-cert -n kcp --timeout=60s
done
```

## Step 8: Grant Workspace Access

Grant each team access to their respective workspace using RBAC.

### Team Alpha Access

```bash
kubectl ws :root:team-alpha
kubectl apply -f - <<EOF
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: team-alpha-admin
subjects:
- kind: Group
  name: team-alpha
  apiGroup: rbac.authorization.k8s.io
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: cluster-admin
EOF
```

### Team Beta Access

```bash
kubectl ws :root:team-beta
kubectl apply -f - <<EOF
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: team-beta-admin
subjects:
- kind: Group
  name: team-beta
  apiGroup: rbac.authorization.k8s.io
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: cluster-admin
EOF
```

### Team Gamma Access

```bash
kubectl ws :root:team-gamma
kubectl apply -f - <<EOF
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: team-gamma-admin
subjects:
- kind: Group
  name: team-gamma
  apiGroup: rbac.authorization.k8s.io
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: cluster-admin
EOF
```

### Team Delta Access

```bash
kubectl ws :root:team-delta
kubectl apply -f - <<EOF
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: team-delta-admin
subjects:
- kind: Group
  name: team-delta
  apiGroup: rbac.authorization.k8s.io
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: cluster-admin
EOF
```

Return to root workspace:

```bash
kubectl ws :root
```

## Step 9: Create Team Kubeconfigs

Extract team certificates and create kubeconfigs for each team.

### Extract Team Certificates

```bash
for team in alpha beta gamma delta; do
  kubectl get secret team-${team}-cert -n kcp \
    -o=jsonpath='{.data.tls\.crt}' | base64 -d > team-${team}.crt
  kubectl get secret team-${team}-cert -n kcp \
    -o=jsonpath='{.data.tls\.key}' | base64 -d > team-${team}.key
done
```

### Create Team Kubeconfigs

```bash
for team in alpha beta gamma delta; do
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
```

## Step 10: Verify Team Access

Test that each team can access their workspace and is isolated from others:

```bash
for team in alpha beta gamma delta; do
  echo "--- team-${team} ---"
  KUBECONFIG=team-${team}.kubeconfig kubectl get namespaces
done
```

Create a namespace in each team workspace to confirm write access:

```bash
for team in alpha beta gamma delta; do
  KUBECONFIG=team-${team}.kubeconfig kubectl get namespace demo-${team} >/dev/null 2>&1 || \
    KUBECONFIG=team-${team}.kubeconfig kubectl create namespace demo-${team}
  KUBECONFIG=team-${team}.kubeconfig kubectl get namespace demo-${team}
done
```

Verify workspace isolation — each team should be denied access to other workspaces:

```bash
# Team Alpha should NOT be able to access Team Beta's workspace
KUBECONFIG=team-alpha.kubeconfig kubectl get namespaces \
  --server https://${KCP_EXTERNAL_HOSTNAME}:${KCP_PORT}/clusters/root:team-beta && \
  echo "ERROR: Team Alpha can access Team Beta (isolation broken)" || \
  echo "OK: Team Alpha cannot access Team Beta (isolation works)"
```

## Summary

You now have a fully functional kcp deployment on kind with:

- **kcp** deployed via Helm with TLS certificates managed by cert-manager
- **4 team workspaces**: team-alpha, team-beta, team-gamma, team-delta
- **4 client certificates**: One for each team with group membership
- **RBAC configuration**: Each team has cluster-admin access to their workspace

Each team can independently:

- Create and manage resources in their workspace
- Deploy applications
- Configure their own RBAC policies within their workspace

## Cleanup

To remove everything:

```bash
# Delete the kind cluster
kind delete cluster --name kcp

# Remove generated files
rm -f ca.crt admin-client.crt admin-client.key admin.kubeconfig
rm -f team-*.crt team-*.key team-*.kubeconfig

# Remove hosts entry (optional)
# macOS:
sudo sed -i '' '/kcp.local.test/d' /etc/hosts
# Linux:
# sudo sed -i '/kcp.local.test/d' /etc/hosts
```

## Next Steps

- [Authorization](../concepts/authorization/index.md) - Learn more about kcp's authorization model
- [Workspaces](../concepts/workspaces/index.md) - Deep dive into workspace concepts
- [APIs](../concepts/apis/index.md) - Learn how to export and bind APIs across workspaces
- [Production Setup](./production/index.md) - Guidelines for production deployments
