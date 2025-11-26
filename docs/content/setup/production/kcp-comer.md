---
description: >
  Deploy kcp with dual front-proxy and edge re-encryption integration for enterprise environments requiring edge acceleration and advanced networking.
---

# kcp-comer: Dual Front-Proxy with Edge re-encryption

The kcp-comer deployment pattern implements a dual front-proxy architecture with CDN integration, designed for enterprise environments requiring global performance, edge acceleration, and advanced networking capabilities. This can be adjusted to work with various CDN providers, but this guide focuses on CloudFlare integration.

## Prerequisites

Ensure all [shared components](prerequisites.md) are installed, plus:

- **CloudFlare account** with API access
- **Custom domain** with CloudFlare DNS management

## Architecture Diagram

```
Internet → CloudFlare Edge → Front-Proxy (External) → Shards
```

## Deployment Steps

### 1. Configure CloudFlare Integration

Set up CloudFlare for edge termination and routing:

**CloudFlare CA Certificate**: Download the CloudFlare edge certificate for extended trust:

!!! note
    Verify this URL with CloudFlare documentation before production use.

```bash
kubectl create namespace kcp-comer
curl -L -o google-we1.pem https://ssl-tools.net/certificates/108fbf794e18ec5347a414e4370cc4506c297ab2.pem
kubectl create secret generic google-we1-ca --from-file=tls.crt=google-we1.pem -n kcp-comer
```

### 2. Create Namespace and etcd Certificates

```bash
kubectl apply -f contrib/production/kcp-comer/certificate-etcd.yaml
```

### 2. Deploy etcd Clusters with Enhanced Configuration

```bash
kubectl apply -f contrib/production/kcp-comer/etcd-druid-root.yaml
kubectl apply -f contrib/production/kcp-comer/etcd-druid-alpha.yaml
```

**Verify etcd deployment**:
```bash
kubectl get etcd -n kcp-comer
kubectl wait --for=condition=Ready etcd -n kcp-comer --all --timeout=300s
```

### 3. Configure kcp System Certificates

Set up multi-tier certificate management:

```bash
kubectl apply -f contrib/production/kcp-comer/certificate-kcp.yaml
```

### 4. Deploy Dual Front-Proxy Architecture

Deploy external and internal front-proxy layers:

```bash
# NOTE: These files need to be customized with your domain names before applying
kubectl apply -f contrib/production/kcp-comer/kcp-root-shard.yaml
kubectl apply -f contrib/production/kcp-comer/kcp-alpha-shard.yaml
kubectl apply -f contrib/production/kcp-comer/kcp-front-proxy.yaml
kubectl apply -f contrib/production/kcp-comer/kcp-front-proxy-internal.yaml
```

4.1. Get the LoadBalancer IP:
```bash
kubectl get svc -n kcp-comer 
```

Configure DNS records in CloudFlare (or your chosen CDN).

4.2 Verify DNS resolution:
```bash
nslookup api.comer.example.com
```

4.3 Verify deployment:
```bash
kubectl get pods -n kcp-comer 
```

### CloudFlare Configuration: 

Configure your CloudFlare dashboard:

1. **Set `api.comer` to "Proxied"** (orange cloud icon)
2. **Add Page Rule**: "Rewrite port to 6443" for the API domain
3. **Upload Custom CA** in SSL/TLS tab so CloudFlare trusts the internal certificate:
   ```bash
   kubectl get secret -n kcp-comer root-ca -o jsonpath='{.data.ca\.crt}' | base64 -d
   ```

### 5. Verify External Access

**Verify the front-proxy is accessible:**
   ```bash
   # Note: No 6443 due to rewrite via CloudFlare
   curl -k https://api.comer.example.com/healthz
   ```


## Important: Certificate Authentication Limitation

Due to CloudFlare's certificate re-encryption, certificate-based authentication through the public front-proxy **will not work**. The certificate presented to clients is CloudFlare's certificate, not the internal front-proxy certificate.

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

So to authenticate with the kcp-comer deployment for outside access, you must use OIDC authentication.


### 6. Create Admin Access and Test

```bash
kubectl apply -f contrib/production/kcp-comer/kubeconfig-kcp-admin.yaml

kubectl get secret -n kcp-comer kcp-admin-frontproxy \
  -o jsonpath='{.data.kubeconfig}' | base64 -d > kcp-admin-kubeconfig-comer.yaml


# If you test this now, it will not work due to note above. Lets configure OIDC first.

KUBECONFIG=kcp-admin-kubeconfig-comer.yaml \
kubectl config set-credentials oidc \
  --exec-api-version=client.authentication.k8s.io/v1beta1 \
  --exec-command=kubectl \
  --exec-arg=oidc-login \
  --exec-arg=get-token \
  --exec-arg=--oidc-issuer-url="https://auth.example.com" \
  --exec-arg=--oidc-client-id="platform-mesh" \
  --exec-arg=--oidc-extra-scope="email" \
  --exec-arg=--oidc-client-secret=Z2Fyc2lha2FsYmlzdmFuZGVuekWplCg==

# And this should redirect to OIDC login flow but fails to list with lack of permissions.
KUBECONFIG=kcp-admin-kubeconfig-comer.yaml kubectl get shards --user oidc
```

Test access using internal front-proxy:

```bash
kubectl apply -f contrib/production/kcp-comer/kubeconfig-kcp-admin-internal.yaml
kubectl get secret -n kcp-comer kcp-admin-frontproxy-internal \
  -o jsonpath='{.data.kubeconfig}' | base64 -d > kcp-admin-kubeconfig-comer-internal.yaml

KUBECONFIG=kcp-admin-kubeconfig-comer-internal.yaml kubectl get shards
```

**Expected output**:
```
KUBECONFIG=kcp-admin-kubeconfig-comer-internal.yaml kubectl get shards                                                                                                                           13:26:14
NAME    REGION   URL                                               EXTERNAL URL                                   AGE
alpha            https://alpha.comer.example.com:6443   https://api.comer.example.com:443   18m
root             https://root.comer.example.com:6443    https://api.comer.example.com:443   21m
```
