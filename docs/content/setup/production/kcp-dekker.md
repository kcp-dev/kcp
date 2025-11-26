---
description: >
  Deploy kcp with self-signed certificates using the kcp-dekker pattern for development and internal environments.
---

# kcp-dekker: Self-Signed Certificate Deployment

The kcp-dekker deployment pattern uses self-signed certificates and is ideal for development, testing, or closed internal environments where certificate trust can be managed centrally.

## Architecture Overview

- **Certificate approach**: All certificates are self-signed using an internal CA
- **Access pattern**: Only front-proxy is publicly accessible, shards are private  
- **Network**: Simple single-cluster deployment with cluster-internal shard communication
- **DNS requirements**: Single public DNS record for the front-proxy endpoint

## Prerequisites

Ensure all [shared components](prerequisites.md) are installed before proceeding.

## DNS Configuration

Create public DNS records for all endpoints:

```bash
# Required DNS records
api.dekker.example.com     → Front-proxy LoadBalancer IP
```

## Deployment Steps

### 1. Create Namespace and etcd Certificates

Create the deployment namespace and configure certificates for etcd clusters:

```bash
kubectl create namespace kcp-dekker
kubectl apply -f contrib/production/kcp-dekker/certificate-etcd.yaml
```

### 2. Deploy etcd Clusters  

Deploy dedicated etcd clusters for root and alpha shards:

```bash
kubectl apply -f contrib/production/kcp-dekker/etcd-druid-root.yaml
kubectl apply -f contrib/production/kcp-dekker/etcd-druid-alpha.yaml
```

**Verify etcd deployment**:
```bash
kubectl get etcd -n kcp-dekker
kubectl wait --for=condition=Ready etcd -n kcp-dekker --all --timeout=300s
```

### 3. Configure kcp System Certificates

Set up certificates for kcp components using the internal CA:

```bash
kubectl apply -f contrib/production/kcp-dekker/certificate-kcp.yaml
```

**Verify certificate issuance**:
```bash
kubectl get certificate -n kcp-dekker
```

### 4. Deploy KCP Components

Deploy the kcp shards and front-proxy:

```bash
# NOTE: These files needs to be customized with your domain names before applying
kubectl apply -f contrib/production/kcp-dekker/kcp-root-shard.yaml
kubectl apply -f contrib/production/kcp-dekker/kcp-alpha-shard.yaml  
kubectl apply -f contrib/production/kcp-dekker/kcp-front-proxy.yaml
```

**Verify kcp deployment**:
```bash
kubectl get pods -n kcp-dekker 
```

### 5. Configure DNS for Front-Proxy

5.1. Get the LoadBalancer IP:
   ```bash
   kubectl get svc -n kcp-dekker frontproxy-front-proxy
   ```

5.2. **Create DNS A record** pointing `api.dekker.example.com` to the LoadBalancer IP

5.3. Verify DNS resolution:
   ```bash
   nslookup api.dekker.example.com
   ```

5.4. Verify certificate issuance (may take a few minutes):
   ```bash
   kubectl get certificate -n kcp-dekker root-frontproxy-server -o yaml
   ```

5.5. Verify the front-proxy is accessible:
   ```bash
   curl -k https://api.dekker.example.com:6443/healthz
   ```

### 6. Create and Test Admin Access

Generate admin kubeconfig and test cluster connectivity:

```bash
kubectl apply -f contrib/production/kcp-dekker/kubeconfig-kcp-admin.yaml

kubectl get secret -n kcp-dekker kcp-admin-frontproxy \
  -o jsonpath='{.data.kubeconfig}' | base64 -d > kcp-admin-kubeconfig-dekker.yaml

KUBECONFIG=kcp-admin-kubeconfig-dekker.yaml kubectl get shards
```

**Expected output**:
```
KUBECONFIG=kcp-admin-kubeconfig-dekker.yaml kubectl get shards                                                                                      12:23:39
NAME    REGION   URL                                                         EXTERNAL URL                                     AGE
alpha            https://alpha-shard-kcp.kcp-dekker.svc.cluster.local:6443   https://api.dekker.example.com:6443   10m
root             https://root-kcp.kcp-dekker.svc.cluster.local:6443          https://api.dekker.example.com:6443   11m
```

### Install kubectl OIDC Plugin

```bash
# Homebrew (macOS and Linux)
brew install kubelogin

# Krew (macOS, Linux, Windows and ARM)
kubectl krew install oidc-login

# Chocolatey (Windows)
choco install kubelogin

# For other platforms, see: https://github.com/int128/kubelogin
```

### Configure OIDC Credentials

```bash
kubectl config set-credentials oidc \
  --exec-api-version=client.authentication.k8s.io/v1beta1 \
  --exec-command=kubectl \
  --exec-arg=oidc-login \
  --exec-arg=get-token \
  --exec-arg=--oidc-issuer-url="https://auth.example.com" \
  --exec-arg=--oidc-client-id="platform-mesh" \
  --exec-arg=--oidc-extra-scope="email" \
  --exec-arg=--oidc-client-secret=Z2Fyc2lha2FsYmlzdmFuZGVuekWplCg==

kubectl config set-context --current --user=oidc

# And this should redirect to OIDC login flow but fails to list with lack of permissions.
KUBECONFIG=kcp-admin-kubeconfig-dekker.yaml kubectl get shards --user oidc
```