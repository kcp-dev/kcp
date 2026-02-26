---
description: >
  Deploy kcp with external certificates using Let's Encrypt for production environments requiring trusted certificates.
---

# kcp-vespucci: External Certificate Deployment

The kcp-vespucci deployment pattern uses external certificates (Let's Encrypt) and is ideal for production environments where trusted certificates are required and both front-proxy and shards need public accessibility.

## Architecture Overview

- **Certificate approach**: Let's Encrypt for automatic certificate management
- **Access pattern**: Both front-proxy and shards are publicly accessible
- **Network**: Multi-zone deployment with external certificate validation
- **DNS requirements**: Multiple public DNS records for front-proxy and each shard

## Prerequisites

Ensure all [shared components](prerequisites.md) are installed before proceeding.

**Additional requirements for kcp-vespucci:**
- Public DNS domain with ability to create multiple A records
- LoadBalancer service capability for multiple endpoints
- Let's Encrypt ACME challenge capability (HTTP-01 or DNS-01)

## Deployment Steps

### 1. Create a DNS Records

Create public DNS records for all endpoints:

```bash
# Required DNS records
api.vespucci.example.com     → Front-proxy LoadBalancer IP
root.vespucci.example.com    → Root shard LoadBalancer IP
alpha.vespucci.example.com   → Alpha shard LoadBalancer IP
```

!! note
    DNS records must be configured before certificate issuance begins.

### 2. Create Namespace

```bash
kubectl create namespace kcp-vespucci
kubectl apply -f contrib/production/kcp-vespucci/certificate-etcd.yaml
```

**Verify issuer is ready**:

This was part of prerequisites but double-check.

```bash
kubectl get clusterissuer letsencrypt-prod
```

### 3. Deploy etcd Clusters

Deploy etcd clusters with external certificate support:

```bash
kubectl apply -f contrib/production/kcp-vespucci/etcd-druid-root.yaml
kubectl apply -f contrib/production/kcp-vespucci/etcd-druid-alpha.yaml
```

**Verify etcd clusters**:
```bash
kubectl get etcd -n kcp-vespucci
kubectl wait --for=condition=Ready etcd -n kcp-vespucci --all --timeout=300s
```

### 4. Configure kcp System Certificates

Set up certificates for kcp components using the internal CA:

```bash
kubectl apply -f contrib/production/kcp-vespucci/certificate-kcp.yaml
```

**Verify certificate issuance**:
```bash
kubectl get certificate -n kcp-vespucci
```

### 5. Deploy kcp Components with External Access

Because we use Let's Encrypt, and since kubectl needs explisit CA configuration, we need to deploy kcp components with extended CA bundle trust. This mighgt be different in your environment.

```bash
curl -L -o isrgrootx1.pem https://letsencrypt.org/certs/isrgrootx1.pem
kubectl create secret generic letsencrypt-ca --from-file=tls.crt=isrgrootx1.pem -n kcp-vespucci
```

Deploy kcp components configured for public shard access:

```bash
# NOTE: These files need to be customized with your domain names before applying
kubectl apply -f contrib/production/kcp-vespucci/kcp-root-shard.yaml
kubectl apply -f contrib/production/kcp-vespucci/kcp-alpha-shard.yaml
kubectl apply -f contrib/production/kcp-vespucci/kcp-front-proxy.yaml
```

**Verify deployment**:
```bash
kubectl get pods -n kcp-vespucci
```

### 6. Verify LoadBalancer Services

Ensure all required LoadBalancer services are provisioned:

```bash
kubectl get svc -n kcp-vespucci -o wide
```

**Expected services**:
```
NAME                           TYPE           EXTERNAL-IP     PORT(S)          AGE
frontproxy-front-proxy        LoadBalancer   203.0.113.10    6443:30001/TCP   5m
root-kcp                      LoadBalancer   203.0.113.11    6443:30002/TCP   5m
alpha-shard-kcp               LoadBalancer   203.0.113.12    6443:30003/TCP   5m
```

### 7. Update DNS Records with LoadBalancer IPs

Update your DNS records with the actual LoadBalancer IP addresses:

```bash
# Get LoadBalancer IPs (or CNAMEs if using DNS-based LoadBalancers)
kubectl get svc -n kcp-vespucci frontproxy-front-proxy -o jsonpath='{.status.loadBalancer}'
kubectl get svc -n kcp-vespucci root-kcp -o jsonpath='{.status.loadBalancer'
kubectl get svc -n kcp-vespucci alpha-shard-kcp -o jsonpath='{.status.loadBalancer}'
```

**Verify DNS propagation**:
```bash
nslookup api.vespucci.example.com
nslookup root.vespucci.example.com
nslookup alpha.vespucci.example.com
```

Verify the front-proxy is accessible:
```bash
curl -k https://api.vespucci.example.com:6443/healthz
```

### 8. Create Admin Access and Test Connectivity

```bash
kubectl apply -f contrib/production/kcp-vespucci/kubeconfig-kcp-admin.yaml

kubectl get secret -n kcp-vespucci kcp-admin-frontproxy \
  -o jsonpath='{.data.kubeconfig}' | base64 -d > kcp-admin-kubeconfig-vespucci.yaml

KUBECONFIG=kcp-admin-kubeconfig-vespucci.yaml kubectl get shards
```

**Expected output**:
```
KUBECONFIG=kcp-admin-kubeconfig-vespucci.yaml kubectl get shards
NAME    REGION   URL                                                  EXTERNAL URL                                       AGE
alpha            https://alpha.vespucci.example.com:6443   https://api.vespucci.example.com:6443   7m46s
root             https://root.vespucci.example.com:6443    https://api.vespucci.example.com:6443   9m23s
```
