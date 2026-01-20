---
description: >
  Deploy kcp with self-signed certificates across multiple regions.
---

# kcp-zheng: Multi-Region Self-Signed Certificate Deployment

The kcp-zheng deployment pattern uses self-signed certificates with an internal CA and is ideal for multi-region deployments across different clouds without shared network. In this scenario we use 3 different Kubernetes clusters for shards, with all shards accessed via external URLs and front-proxy as the only public endpoint.

## Architecture Overview

- **Certificate approach**: All certificates are self-signed using an internal CA
- **Access pattern**: Only front-proxy is publicly accessible, shards have external URLs for cross-region access
- **Network**: 3 Kubernetes cluster deployments in different clouds without shared network
- **DNS requirements**: Public DNS records for front-proxy and each shard

## Prerequisites

Ensure all [shared components](prerequisites.md) are installed before proceeding.

**Additional requirements for kcp-zheng:**
- Public DNS domain with ability to create multiple A records
- LoadBalancer service capability for front-proxy and shard endpoints
- External network connectivity between clusters

## Deployment Steps

### 1. Create DNS Records

Create public DNS records for all endpoints:

```bash
# Required DNS records
api.zheng.example.io    → Front-proxy LoadBalancer IP
root.zheng.example.io   → Root shard LoadBalancer IP
alpha.zheng.example.io  → Alpha shard LoadBalancer IP (cluster 2)
beta.zheng.example.io   → Beta shard LoadBalancer IP (cluster 3)
```

!!! note
    DNS records must be configured before proceeding with deployment.

---

## Cluster 1: Deploy Root Shard and Front-Proxy

### 2. Create Namespace and Certificate Issuer

On cluster 1, where the operator is running and root shard will be deployed:

```bash
kubectl create namespace kcp-zheng
kubectl apply -f contrib/production/etcd-druid/certificate-etcd-issuer.yaml
kubectl apply -f contrib/production/kcp-zheng/certificate-etcd-root.yaml
```

**Verify issuer is ready**:

```bash
kubectl get issuer -n kcp-zheng
```

### 3. Deploy etcd Cluster

Deploy etcd cluster with self-signed certificates:

```bash
kubectl apply -f contrib/production/kcp-zheng/etcd-druid-root.yaml
```

**Verify etcd cluster**:
```bash
kubectl get etcd -n kcp-zheng
kubectl wait --for=condition=Ready etcd -n kcp-zheng --all --timeout=300s
```

### 4. Configure KCP System Certificates

Set up certificates for kcp components using the internal CA:

```bash
kubectl apply -f contrib/production/kcp-zheng/certificate-kcp.yaml
```

**Verify certificate issuance**:
```bash
kubectl get certificate -n kcp-zheng
```

Because we use Let's Encrypt for the front-proxy, and since kubectl needs explicit CA configuration, we need to deploy kcp components with extended CA bundle trust:

```bash
curl -L -o isrgrootx1.pem https://letsencrypt.org/certs/isrgrootx1.pem
kubectl create secret generic letsencrypt-ca --from-file=tls.crt=isrgrootx1.pem -n kcp-zheng
```

### 5. Deploy KCP Components

Deploy kcp components:

```bash
# NOTE: These files need to be customized with your domain name before applying
kubectl apply -f contrib/production/kcp-zheng/kcp-root-shard.yaml
kubectl apply -f contrib/production/kcp-zheng/kcp-front-proxy.yaml
```

**Verify deployment**:
```bash
kubectl get pods -n kcp-zheng
```

### 6. Verify Services

Ensure the front-proxy LoadBalancer is provisioned:

```bash
kubectl get svc -n kcp-zheng -o wide
```

**Expected services**:
```
NAME                     TYPE           EXTERNAL-IP     PORT(S)          AGE
frontproxy-front-proxy   LoadBalancer   203.0.113.10    6443:30001/TCP   5m
root-kcp                 LoadBalancer   203.0.113.11    6443:30002/TCP   5m
```

### 7. Update DNS Records with LoadBalancer IPs

Update your DNS records with the LoadBalancer IP addresses:

```bash
kubectl get svc -n kcp-zheng frontproxy-front-proxy -o jsonpath='{.status.loadBalancer}'
kubectl get svc -n kcp-zheng root-kcp -o jsonpath='{.status.loadBalancer}'
```

**Verify DNS propagation**:
```bash
nslookup api.zheng.example.io
nslookup root.zheng.example.io
```

Verify the front-proxy is accessible:
```bash
curl -k https://api.zheng.example.io:6443/healthz
```

### 8. Create Admin Access and Test Connectivity

```bash
kubectl apply -f contrib/production/kcp-zheng/kubeconfig-kcp-admin.yaml

kubectl get secret -n kcp-zheng kcp-admin-frontproxy \
  -o jsonpath='{.data.kubeconfig}' | base64 -d > kcp-admin-kubeconfig-zheng.yaml

KUBECONFIG=kcp-admin-kubeconfig-zheng.yaml kubectl get shards
```

**Expected output**:
```
NAME   REGION   URL                                    EXTERNAL URL                          AGE
root            https://root.zheng.example.io:6443    https://api.zheng.example.io:6443     3m20s
```

### 9. Create Alpha and Beta Shard Bundles

Now configure the root cluster to generate alpha and beta shard bundles:

```bash
kubectl apply -f contrib/production/kcp-zheng/kcp-alpha-shard.yaml
kubectl apply -f contrib/production/kcp-zheng/kcp-beta-shard.yaml
```

**Verify bundles are created** (shards should NOT be running yet):
```bash
kubectl get deployments.apps -n kcp-zheng
```

**Expected output**:
```
NAME                     READY   UP-TO-DATE   AVAILABLE   AGE
alpha-shard-kcp          0/0     0            0           2m
beta-shard-kcp           0/0     0            0           2m
frontproxy-front-proxy   2/2     2            2           31m
root-kcp                 2/2     2            2           31m
root-proxy               2/2     2            2           31m
```

**Verify bundles are ready**:
```bash
kubectl get bundles.operator.kcp.io -A
```

**Expected output**:
```
NAMESPACE   NAME           TARGET   PHASE   AGE
kcp-zheng   alpha-bundle            Ready   2m
kcp-zheng   beta-bundle             Ready   2m
```

---

## Cluster 2: Deploy Alpha Shard

Now move to cluster 2 and deploy the alpha shard using the generated bundle.

### 1. Create Namespace

```bash
kubectl create namespace kcp-zheng
```

### 2. Install Prerequisites

Install etcd operator:
```bash
helm install etcd-druid oci://europe-docker.pkg.dev/gardener-project/releases/charts/gardener/etcd-druid \
  --namespace etcd-druid \
  --create-namespace \
  --version v0.33.0

kubectl apply -f contrib/production/etcd-druid/etcdcopybackupstasks.druid.gardener.cloud.yaml
kubectl apply -f contrib/production/etcd-druid/etcds.druid.gardener.cloud.yaml
```

Install cert-manager if not already installed:
```bash
helm repo add jetstack https://charts.jetstack.io
helm repo update

helm upgrade \
  --install \
  --namespace cert-manager \
  --create-namespace \
  --version v1.18.2 \
  --set crds.enabled=true \
  --atomic \
  cert-manager jetstack/cert-manager
```

### 3. Deploy etcd Issuers and Certificates

```bash
kubectl apply -f contrib/production/etcd-druid/certificate-etcd-issuer.yaml
kubectl apply -f contrib/production/kcp-zheng/certificate-etcd-alpha.yaml
```

### 4. Deploy etcd Cluster

```bash
kubectl apply -f contrib/production/kcp-zheng/etcd-druid-alpha.yaml
```

**Verify etcd cluster**:
```bash
kubectl get etcd -n kcp-zheng
kubectl wait --for=condition=Ready etcd -n kcp-zheng --all --timeout=300s
```

### 5. Deploy Alpha Shard from Bundle

Once etcd is ready, deploy the alpha shard using the generated bundle from cluster 1.

On cluster 1, export the alpha bundle secret:
```bash
kubectl get secret -n kcp-zheng alpha-bundle -o yaml > alpha-bundle.yaml
```

Copy the `alpha-bundle.yaml` file to cluster 2 and apply it:
```bash
kubectl apply -f alpha-bundle.yaml
```

Deploy resources from the bundle secret:
```bash
../kcp-operator/_build/bundler --bundle-name alpha-bundle --bundle-namespace kcp-zheng
```

**Verify shard is running**:
```bash
kubectl get pods -n kcp-zheng
```

**Expected output**:
```
NAME                               READY   STATUS    RESTARTS   AGE
alpha-0                            2/2     Running   0          9m
alpha-1                            2/2     Running   0          9m
alpha-2                            2/2     Running   0          9m
alpha-shard-kcp-69db8985bf-hllmw   1/1     Running   0          90s
alpha-shard-kcp-69db8985bf-qzftr   1/1     Running   0          90s
```

### 6. Configure DNS for Alpha Shard

Get the LoadBalancer IP:
```bash
kubectl get svc -n kcp-zheng alpha-shard-kcp
```

Add DNS record `alpha.zheng.example.io` pointing to the shard LoadBalancer IP.

**Verify DNS propagation**:
```bash
nslookup alpha.zheng.example.io
```

### 7. Verify Alpha Shard Joined

From any machine with the admin kubeconfig:
```bash
KUBECONFIG=kcp-admin-kubeconfig-zheng.yaml kubectl get shards
```

**Expected output**:
```
NAME    REGION   URL                                     EXTERNAL URL                          AGE
alpha            https://alpha.zheng.example.io:6443    https://api.zheng.example.io:6443     2m
root             https://root.zheng.example.io:6443     https://api.zheng.example.io:6443     38m
```

---

## Cluster 3: Deploy Beta Shard

Repeat the same steps as cluster 2 for the beta shard.

### 1. Create Namespace

```bash
kubectl create namespace kcp-zheng
```

### 2. Install Prerequisites

Install etcd operator:
```bash
helm install etcd-druid oci://europe-docker.pkg.dev/gardener-project/releases/charts/gardener/etcd-druid \
  --namespace etcd-druid \
  --create-namespace \
  --version v0.33.0

kubectl apply -f contrib/production/etcd-druid/etcdcopybackupstasks.druid.gardener.cloud.yaml
kubectl apply -f contrib/production/etcd-druid/etcds.druid.gardener.cloud.yaml
```

Install cert-manager if not already installed:
```bash
helm repo add jetstack https://charts.jetstack.io
helm repo update

helm upgrade \
  --install \
  --namespace cert-manager \
  --create-namespace \
  --version v1.18.2 \
  --set crds.enabled=true \
  --atomic \
  cert-manager jetstack/cert-manager
```

### 3. Deploy etcd Issuers and Certificates

```bash
kubectl apply -f contrib/production/etcd-druid/certificate-etcd-issuer.yaml
kubectl apply -f contrib/production/kcp-zheng/certificate-etcd-beta.yaml
```

### 4. Deploy etcd Cluster

```bash
kubectl apply -f contrib/production/kcp-zheng/etcd-druid-beta.yaml
```

**Verify etcd cluster**:
```bash
kubectl get etcd -n kcp-zheng
kubectl wait --for=condition=Ready etcd -n kcp-zheng --all --timeout=300s
```

### 5. Deploy Beta Shard from Bundle

On cluster 1, export the beta bundle secret:
```bash
kubectl get secret -n kcp-zheng beta-bundle -o yaml > beta-bundle.yaml
```

Copy the `beta-bundle.yaml` file to cluster 3 and apply it:
```bash
kubectl apply -f beta-bundle.yaml
```

Deploy resources from the bundle secret:
```bash
../kcp-operator/_build/bundler --bundle-name beta-bundle --bundle-namespace kcp-zheng
```

**Verify shard is running**:
```bash
kubectl get pods -n kcp-zheng
```

### 6. Configure DNS for Beta Shard

Get the LoadBalancer IP:
```bash
kubectl get svc -n kcp-zheng beta-shard-kcp
```

Add DNS record `beta.zheng.example.io` pointing to the shard LoadBalancer IP.

**Verify DNS propagation**:
```bash
nslookup beta.zheng.example.io
```

### 7. Verify All Shards Joined

From any machine with the admin kubeconfig:
```bash
KUBECONFIG=kcp-admin-kubeconfig-zheng.yaml kubectl get shards
```

**Expected output**:
```
NAME    REGION   URL                                     EXTERNAL URL                          AGE
alpha            https://alpha.zheng.example.io:6443    https://api.zheng.example.io:6443     15m
beta             https://beta.zheng.example.io:6443     https://api.zheng.example.io:6443     2m
root             https://root.zheng.example.io:6443     https://api.zheng.example.io:6443     50m
```
