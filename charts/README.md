# KCP with TMC Helm Charts

This directory contains Helm charts for deploying KCP (Kubernetes Control Plane) with TMC (Transparent Multi-Cluster) support in production environments.

## 📦 Available Charts

### 1. `kcp-tmc` - KCP Server with TMC Components
**Location**: `./kcp-tmc/`
**Purpose**: Deploys the KCP control plane with all TMC components enabled

**Key Features**:
- Complete KCP server with TMC support
- Error handling, health monitoring, metrics, and recovery
- Virtual workspace management and placement controller
- Production-ready configuration with persistence and RBAC
- Monitoring integration with Prometheus
- Configurable resource limits and scaling

### 2. `kcp-syncer` - Workload Syncer for Target Clusters  
**Location**: `./kcp-syncer/`
**Purpose**: Deploys syncers to target clusters for workload synchronization

**Key Features**:
- Bidirectional resource synchronization
- TMC-aware syncer with enhanced capabilities
- Secure kubeconfig management
- Health monitoring and metrics
- Support for multiple sync targets per cluster

## 🚀 Quick Start

### Prerequisites

```bash
# Required tools
- Kubernetes 1.26+
- Helm 3.8+
- Container registry access
- kubectl configured for target clusters

# Build TMC-enabled images (see BUILD-TMC.md)
make build
docker build -f docker/Dockerfile.tmc --target kcp-server -t your-registry/kcp-server:v0.11.0 .
docker build -f docker/Dockerfile.tmc --target workload-syncer -t your-registry/kcp-syncer:v0.11.0 .
docker push your-registry/kcp-server:v0.11.0
docker push your-registry/kcp-syncer:v0.11.0
```

### 1. Deploy KCP Host

```bash
# Install KCP with TMC on the host cluster
helm install kcp-tmc ./kcp-tmc \
  --namespace kcp-system \
  --create-namespace \
  --set global.imageRegistry=your-registry \
  --set kcp.image.tag=v0.11.0 \
  --set kcp.tmc.enabled=true \
  --set kcp.persistence.enabled=true \
  --set monitoring.enabled=true
```

### 2. Deploy Syncers to Target Clusters

```bash
# Deploy syncer to a target cluster
helm install kcp-syncer-prod ./kcp-syncer \
  --kube-context target-cluster \
  --namespace kcp-syncer \
  --create-namespace \
  --set global.imageRegistry=your-registry \
  --set syncer.image.tag=v0.11.0 \
  --set syncer.syncTarget.name=production-cluster \
  --set syncer.kcp.endpoint=https://kcp.company.com:6443 \
  --set-file syncer.kcp.kubeconfig=kcp-admin.kubeconfig \
  --set-file syncer.cluster.kubeconfig=target-cluster.kubeconfig
```

## 📖 Detailed Configuration

### KCP TMC Chart Configuration

#### Essential Settings

```yaml
# values.yaml for kcp-tmc
global:
  imageRegistry: "your-registry.com"

kcp:
  image:
    tag: "v0.11.0"
  
  # TMC Components
  tmc:
    enabled: true
    errorHandling:
      enabled: true
    healthMonitoring:
      enabled: true
    metrics:
      enabled: true
    recovery:
      enabled: true
    virtualWorkspaces:
      enabled: true
    placementController:
      enabled: true

  # Production settings
  persistence:
    enabled: true
    size: 50Gi
    storageClass: "fast-ssd"
  
  resources:
    requests:
      cpu: 1000m
      memory: 2Gi
    limits:
      cpu: 2000m
      memory: 4Gi

# Monitoring
monitoring:
  enabled: true
  prometheus:
    serviceMonitor:
      enabled: true
      labels:
        release: prometheus

# RBAC
rbac:
  create: true
```

#### Service Exposure Options

```yaml
# NodePort (for local/development)
kcp:
  service:
    type: NodePort

# LoadBalancer (for cloud environments)
kcp:
  service:
    type: LoadBalancer

# Ingress (for advanced routing)
ingress:
  enabled: true
  className: "nginx"
  hosts:
    - host: kcp.company.com
      paths:
        - path: /
          pathType: Prefix
  tls:
    - secretName: kcp-tls
      hosts:
        - kcp.company.com
```

### Syncer Chart Configuration

#### Basic Syncer Setup

```yaml
# values.yaml for kcp-syncer
global:
  imageRegistry: "your-registry.com"

syncer:
  image:
    tag: "v0.11.0"
    
  # Target configuration
  syncTarget:
    name: "production-east"
    workspace: "root:production"
    
  # KCP connection
  kcp:
    endpoint: "https://kcp.company.com:6443"
    kubeconfigSecret:
      name: "kcp-admin-kubeconfig"
      
  # Target cluster connection
  cluster:
    kubeconfigSecret:
      name: "target-cluster-kubeconfig"

  # Performance tuning
  config:
    workers: 20
    syncInterval: "15s"
    resyncPeriod: "5m"
    
  resources:
    requests:
      cpu: 200m
      memory: 256Mi
    limits:
      cpu: 1000m
      memory: 1Gi

# Monitoring
monitoring:
  enabled: true
  serviceMonitor:
    enabled: true
```

#### Multi-Syncer per Cluster

```yaml
# Deploy multiple syncers for different workspaces
# syncer-production.yaml
syncer:
  syncTarget:
    name: "production-workloads"
    workspace: "root:production"

---
# syncer-staging.yaml  
syncer:
  syncTarget:
    name: "staging-workloads"
    workspace: "root:staging"
```

## 🔧 Production Deployment Patterns

### High Availability KCP

```yaml
# HA configuration
kcp:
  # Use external database for HA
  persistence:
    enabled: true
    storageClass: "replicated-storage"
    size: 100Gi

  # Resource allocation for HA
  resources:
    requests:
      cpu: 2000m
      memory: 4Gi
    limits:
      cpu: 4000m
      memory: 8Gi

# Pod disruption budget
podDisruptionBudget:
  enabled: true
  minAvailable: 1

# Horizontal pod autoscaling
autoscaling:
  enabled: true
  minReplicas: 2
  maxReplicas: 5
  targetCPUUtilizationPercentage: 70
```

### Multi-Region Deployment

```bash
# Deploy KCP in primary region
helm install kcp-tmc ./kcp-tmc \
  --namespace kcp-system \
  --values production-values.yaml \
  --set kcp.config.extraArgs[0]="--region=us-west-2"

# Deploy syncers in multiple regions
for region in us-east-1 eu-west-1 ap-southeast-1; do
  helm install kcp-syncer-${region} ./kcp-syncer \
    --kube-context ${region}-cluster \
    --namespace kcp-syncer \
    --set syncer.syncTarget.name=${region}-cluster \
    --set syncer.syncTarget.workspace=root:${region} \
    --values syncer-${region}-values.yaml
done
```

### GitOps Integration

```yaml
# ArgoCD Application for KCP
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: kcp-tmc
  namespace: argocd
spec:
  project: platform
  source:
    repoURL: https://github.com/company/kcp-deployments
    targetRevision: main
    path: charts/kcp-tmc
    helm:
      valueFiles:
        - values-production.yaml
  destination:
    server: https://kubernetes.default.svc
    namespace: kcp-system
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
```

## 📊 Monitoring and Observability

### Prometheus Integration

The charts include ServiceMonitor resources for Prometheus Operator:

```yaml
# Automatically discovered metrics
- kcp_tmc_*           # TMC component metrics
- syncer_*            # Syncer operation metrics
- workqueue_*         # Controller workqueue metrics
- process_*           # Process metrics
- go_*               # Go runtime metrics
```

### Grafana Dashboards

Pre-built dashboards are available for:
- KCP TMC Overview
- Syncer Performance
- Cross-Cluster Operations
- Error Analysis and Recovery

### Alert Rules

Example Prometheus alert rules:

```yaml
groups:
- name: kcp-tmc
  rules:
  - alert: KCPDown
    expr: up{job="kcp-tmc"} == 0
    for: 1m
    labels:
      severity: critical
    annotations:
      summary: "KCP server is down"
      
  - alert: SyncerDisconnected
    expr: syncer_connected == 0
    for: 5m
    labels:
      severity: warning
    annotations:
      summary: "Syncer {{ $labels.cluster }} disconnected"
```

## 🔐 Security Considerations

### RBAC Configuration

```yaml
# Minimal RBAC for syncer
rbac:
  create: true
  # Additional cluster roles can be defined
  extraClusterRoles:
    - name: custom-resources
      rules:
        - apiGroups: ["custom.company.com"]
          resources: ["*"]
          verbs: ["*"]
```

### Network Policies

```yaml
# Network isolation for KCP
networkPolicy:
  enabled: true
  ingress:
    - from:
        - namespaceSelector:
            matchLabels:
              name: kcp-syncer
      ports:
        - protocol: TCP
          port: 6443
```

### Secret Management

```bash
# External secret management
kubectl create secret generic kcp-admin-kubeconfig \
  --from-file=kubeconfig=admin.kubeconfig \
  --namespace=kcp-syncer

# Use with syncer chart
helm install kcp-syncer ./kcp-syncer \
  --set syncer.kcp.kubeconfigSecret.name=kcp-admin-kubeconfig
```

## 🧪 Development and Testing

### Local Development

```bash
# Quick local setup with kind
./scripts/helm-demo.sh

# Or manual setup
kind create cluster --name kcp-host
helm install kcp-tmc ./kcp-tmc \
  --set development.enabled=true \
  --set kcp.service.type=NodePort
```

### Testing Deployments

```bash
# Validate chart templates
helm template kcp-tmc ./kcp-tmc --values test-values.yaml

# Dry run installation
helm install kcp-tmc ./kcp-tmc --dry-run --debug

# Test with different configurations
helm test kcp-tmc
```

## 📚 Documentation References

- **[Build Guide](../BUILD-TMC.md)** - Building KCP with TMC
- **[Production Demo](../helm-deployment-demo.md)** - Complete deployment walkthrough
- **[TMC Documentation](../docs/content/developers/tmc/)** - Detailed TMC component docs
- **[Tutorial Collection](../simple-tutorial/)** - Interactive learning tutorials

## 🆘 Troubleshooting

### Common Issues

**Chart installation fails**:
```bash
# Check Helm version and cluster access
helm version
kubectl cluster-info

# Validate values
helm template ./kcp-tmc --values your-values.yaml
```

**Images not found**:
```bash
# Verify registry and tags
docker pull your-registry/kcp-server:v0.11.0
docker pull your-registry/kcp-syncer:v0.11.0
```

**Syncer connection issues**:
```bash
# Check kubeconfig secrets
kubectl get secret -n kcp-syncer
kubectl logs -n kcp-syncer deployment/kcp-syncer-name
```

**TMC components not starting**:
```bash
# Check feature flags
kubectl describe deployment kcp-tmc -n kcp-system
kubectl logs -n kcp-system deployment/kcp-tmc
```

### Getting Help

1. **Check logs**: `kubectl logs -n <namespace> <pod-name>`
2. **Verify configuration**: `helm get values <release-name>`
3. **Review status**: `helm status <release-name>`
4. **Test connectivity**: Use the provided health check scripts

For additional support, refer to the [TMC documentation](../docs/content/developers/tmc/) or run the [interactive demo](../scripts/helm-demo.sh) to see a working example.