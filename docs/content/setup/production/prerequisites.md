---
description: >
  Shared components and prerequisites required for all kcp production deployments.
---

# Prerequisites

Before deploying any kcp production variant, you must install shared components that all deployments depend on. This guide covers the installation and configuration of these foundational components.

- A Kubernetes cluster with sufficient resources
- `kubectl` configured to access your cluster  
- `helm` CLI tool installed
- DNS management capability (manual or automated)
- (Optional) CloudFlare account for DNS01 challenges

## Required Components

All kcp production deployments require:

1. **etcd-druid operator** - Database storage management
2. **cert-manager** - Certificate lifecycle management  
3. **kcp-operator** - kcp resource lifecycle management
4. **OIDC provider (dex)** - Authentication services
5. **DNS configuration** - Domain name resolution
6. **LoadBalancer service** - External traffic routing

## Installation Steps

### 1. etcd-druid Operator

etcd-druid manages etcd clusters for kcp shards with automated backup, restore, and scaling capabilities.

```bash
# Install etcd-druid operator
helm install etcd-druid oci://europe-docker.pkg.dev/gardener-project/releases/charts/gardener/etcd-druid \
  --namespace etcd-druid \
  --create-namespace \
  --version v0.33.0
```

### Install Required CRDs

**Known Issue**: The etcd-druid chart doesn't install CRDs automatically. Install them manually:
([Issue #1185](https://github.com/gardener/etcd-druid/issues/1185)).
Once #1185 is released, this step can be skipped.

```bash
kubectl apply -f contrib/production/etcd-druid/etcdcopybackupstasks.druid.gardener.cloud.yaml
kubectl apply -f contrib/production/etcd-druid/etcds.druid.gardener.cloud.yaml
```

### 2. cert-manager

cert-manager automates certificate management for TLS encryption throughout the kcp deployment.

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

Optional:

We gonna use the CloudFlare DNS01 challenge solver for Let's Encrypt certificates in some deployment variants. If you plan to use CloudFlare, install the cert-manager CloudFlare DNS01 solver:

```bash
cp contrib/production/cert-manager/cluster-issuer.yaml.template kcp/assets/cert-manager/cluster-issuer.yaml
# Edit kcp/assets/cert-manager/cluster-issuer.yaml to add your Email.
kubectl apply -f kcp/assets/cert-manager/cluster-issuer.yaml

cp contrib/production/cert-manager/cloudflare-secret.yaml.template kcp/assets/cert-manager/cloudflare-secret.yaml
# Edit kcp/assets/cert-manager/cloudflare-secret.yaml to add your CloudFlare API token.
kubectl apply -f kcp/assets/cert-manager/cloudflare-secret.yaml
```

### 3. kcp-operator

The kcp-operator manages kcp resource lifecycle and ensures proper configuration.

```bash
helm repo add kcp https://kcp-dev.github.io/helm-charts

helm upgrade --install \
  --create-namespace \
  --namespace kcp-operator \
  kcp-operator kcp/kcp-operator
```

### 4. (Optional) OIDC Provider (Dex)

If you have an existing OIDC provider, you can skip this section. This guide uses Dex as the OIDC provider with PostgreSQL as the backend database.

### 4.1. Install PostgreSQL Operator

Create the OIDC namespace and install CloudNative PostgreSQL operator:

```bash
kubectl create namespace oidc

kubectl apply --server-side -f \
  https://raw.githubusercontent.com/cloudnative-pg/cloudnative-pg/release-1.26/releases/cnpg-1.26.0.yaml
```

### 4.2. Deploy PostgreSQL Database

Create a PostgreSQL cluster and database for Dex:

```bash
kubectl apply -f contrib/production/oidc-dex/postgres-cluster.yaml
kubectl apply -f contrib/production/oidc-dex/postgres-database.yaml
```

### 4.3 Configure Dex Certificates

Request a certificate for Dex from cert-manager:

```bash
kubectl apply -f contrib/production/oidc-dex/certificate-dns.yaml
```

### 4.4 Deploy Dex

```bash
# Check certificate status
kubectl get certificate -n oidc

# Once the certificate is ready, generate Dex values file
cp contrib/production/oidc-dex/values.yaml.template contrib/production/oidc-dex/values.yaml
# Edit contrib/production/oidc-dex/values.yaml to set the correct domain name for Dex and database credentials.

helm repo add dex https://charts.dexidp.io

helm upgrade -i dex dex/dex \
  --create-namespace \
  --namespace oidc \
  -f contrib/production/oidc-dex/values.yaml
``` 

### 5. DNS Configuration

Each deployment type requires specific DNS records. The exact requirements depend on your chosen variant:

#### kcp-dekker (Self-Signed)
```
api.dekker.example.com → LoadBalancer IP
```

#### kcp-vespucci (External Certs)
```
api.vespucci.example.com → LoadBalancer IP
root.vespucci.example.com → LoadBalancer IP  
alpha.vespucci.example.com → LoadBalancer IP
```

#### kcp-comer (Dual Front-Proxy)
```
api.comer.example.com → CDN/LoadBalancer IP
root.comer.example.com → Internal LoadBalancer IP
alpha.comer.example.com → Internal LoadBalancer IP
```

## Verification Steps

After installing all prerequisites, verify the installation:

```bash
# Check all required namespaces exist
kubectl get namespaces | grep -E "(cert-manager|etcd-druid|kcp-operator|dex)"

# Verify operators are running
kubectl get pods -A | grep -E "(cert-manager|etcd-druid|kcp-operator|dex)"
```

## Resource Requirements

Minimum recommended resources for shared components:

| Component | CPU | Memory | Storage |
|-----------|-----|--------|---------|
| etcd-druid | 100m | 128Mi | - |
| cert-manager | 100m | 128Mi | - |  
| kcp-operator | 100m | 128Mi | - |
| dex | 100m | 64Mi | - |
| **Total** | **400m** | **448Mi** | - |

## Security Considerations

1. **Network policies**: Implement network policies to restrict communication between components
2. **RBAC**: Configure minimal required permissions for each component
3. **Secret management**: Use external secret management systems in production
4. **Certificate rotation**: Configure automatic certificate rotation policies
5. **Backup encryption**: Ensure etcd backups are encrypted at rest

## Next Steps

Once all prerequisites are installed and verified:

1. Choose your deployment variant:
   - [kcp-dekker](kcp-dekker.md) - Self-signed certificates
   - [kcp-vespucci](kcp-vespucci.md) - External certificates
   - [kcp-comer](kcp-comer.md) - Dual front-proxy

2. Follow the specific deployment guide for your chosen variant
