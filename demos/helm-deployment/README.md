# TMC Helm Deployment Demo

This demo demonstrates production-ready deployment of KCP with TMC using Helm charts, showcasing enterprise-grade configuration management and operational patterns.

## 🎯 What You'll Learn

- Production Helm chart deployment patterns
- Values-driven TMC configuration management
- Enterprise-ready resource allocation and limits
- Helm-based upgrade and rollback operations
- GitOps-compatible TMC deployments
- Production observability and monitoring setup

## 📋 Prerequisites

- **Docker** 20.10+ (running)
- **kubectl** 1.26+
- **kind** 0.17+
- **helm** 3.8+
- **bash** (for running scripts)

**System Requirements**:
- 8GB+ available RAM
- 20GB+ free disk space
- Internet connection for pulling images

## 🎬 Demo Scenario

**The Challenge**: You need to deploy TMC in a production environment with:
- Proper resource management and limits
- Configuration management through values files
- Easy upgrade and rollback capabilities
- Integration with CI/CD pipelines
- Monitoring and observability

**Helm Solution**: 
- Production-ready Helm charts for KCP with TMC
- Configurable syncer deployments
- Template-driven workload management
- Standard Kubernetes deployment patterns
- Enterprise-grade operational patterns

## 🚀 Quick Start

```bash
# Run the complete demo
./run-demo.sh

# Or run with debug output
DEMO_DEBUG=true ./run-demo.sh

# Keep resources for exploration
DEMO_SKIP_CLEANUP=true ./run-demo.sh
```

## 📁 Demo Contents

```
helm-deployment/
├── README.md                    # This file
├── run-demo.sh                 # Main demo script
├── cleanup.sh                  # Cleanup script
├── validate-demo.sh            # Validation script
├── configs/                    # Cluster configurations
│   ├── kcp-host-config.yaml
│   ├── east-cluster-config.yaml
│   └── west-cluster-config.yaml
├── manifests/                  # Helm values and charts
│   ├── demo-kcp-chart/         # Demo KCP chart
│   ├── demo-syncer-chart/      # Demo syncer chart
│   ├── demo-workload/          # Demo workload chart
│   ├── demo-kcp-values.yaml
│   ├── east-syncer-values.yaml
│   ├── west-syncer-values.yaml
│   ├── east-workload-values.yaml
│   └── west-workload-values.yaml
└── logs/                       # Demo execution logs
```

## 🔄 Demo Flow

### Step 1: Cluster Setup
- Creates KCP host + east/west clusters with unique naming
- Configures production-ready cluster settings
- Sets up proper networking and storage

### Step 2: Helm Chart Validation
- Validates KCP-TMC and syncer Helm charts
- Runs helm lint to ensure chart quality
- Verifies template rendering

### Step 3: KCP-TMC Deployment
- Deploys KCP with TMC using production Helm chart
- Configures proper resource limits and requests
- Sets up health checks and monitoring

### Step 4: Syncer Installation
- Installs syncers using dedicated Helm charts
- Configures cluster-specific settings via values
- Establishes secure connections to KCP

### Step 5: Workload Deployment
- Deploys demo workloads using custom Helm charts
- Demonstrates template-driven configuration
- Shows multi-environment deployment patterns

### Step 6: Operational Demonstrations
- Shows Helm upgrade and rollback operations
- Demonstrates configuration changes via values
- Displays monitoring and observability features

## 🎮 Interactive Features

### Helm Release Management
```bash
=== Helm Releases Status ===
┌─────────────────┬─────────────┬─────────┬─────────────┬──────────────┐
│ Release         │ Cluster     │ Chart   │ Status      │ Revision     │
├─────────────────┼─────────────┼─────────┼─────────────┼──────────────┤
│ kcp-tmc         │ KCP         │ kcp-tmc │ deployed    │ 1            │
│ east-syncer     │ East        │ syncer  │ deployed    │ 1            │
│ west-syncer     │ West        │ syncer  │ deployed    │ 1            │
│ east-workload   │ East        │ demo    │ deployed    │ 1            │
│ west-workload   │ West        │ demo    │ deployed    │ 1            │
└─────────────────┴─────────────┴─────────┴─────────────┴──────────────┘

📊 Resource utilization and health status updated every 10 seconds
```

### Production Operations Dashboard
```bash
=== TMC Production Operations ===
🔧 Configuration Management:
  • Values-driven deployment ✅
  • Template validation ✅
  • Resource limits applied ✅
  • Security contexts configured ✅

📈 Operational Capabilities:
  • Helm upgrade ready ✅
  • Rollback capability ✅
  • Multi-environment config ✅
  • GitOps compatibility ✅

🔄 Upgrade Commands Ready:
  helm upgrade kcp-tmc ./charts/kcp-tmc -f new-values.yaml
  helm upgrade east-syncer ./charts/kcp-syncer -f updated-values.yaml
```

## 🧪 What the Demo Shows

### 1. Production Helm Chart Structure
```yaml
# kcp-tmc-values.yaml
kcp:
  image:
    repository: kcp-dev/kcp
    tag: latest
    pullPolicy: Always
  
  server:
    replicas: 1
    resources:
      requests:
        memory: "512Mi"
        cpu: "200m"
      limits:
        memory: "1Gi"
        cpu: "500m"

tmc:
  enabled: true
  syncers:
    enabled: true
    resources:
      requests:
        memory: "256Mi"
        cpu: "100m"
      limits:
        memory: "512Mi" 
        cpu: "300m"
```

### 2. Template-Driven Configuration
```yaml
# Syncer values with environment-specific settings
syncer:
  syncTarget:
    name: "{{ .Values.cluster.name }}"
    workspace: "root:{{ .Values.cluster.region }}"
  
  kcp:
    endpoint: "{{ .Values.kcp.endpoint }}"
    insecure: {{ .Values.kcp.insecure }}
  
  resources:
    {{- toYaml .Values.resources | nindent 4 }}

labels:
  region: "{{ .Values.cluster.region }}"
  zone: "{{ .Values.cluster.zone }}"
  demo: "{{ .Values.global.demo }}"
```

### 3. Operational Commands
```bash
# Upgrade KCP with new configuration
helm upgrade kcp-tmc ./charts/kcp-tmc \
  --set kcp.server.replicas=3 \
  --set tmc.config.logLevel=debug

# Scale east workload
helm upgrade east-workload ./demo-workload \
  --set replicaCount=5 \
  --set resources.requests.cpu=200m

# Rollback if issues occur
helm rollback east-workload 1
```

## 🔧 Configuration Options

### Environment Variables
```bash
# Demo behavior
DEMO_DEBUG=true                    # Enable debug output
DEMO_SKIP_CLEANUP=true             # Keep resources after demo
DEMO_PAUSE_STEPS=false             # Run without pauses

# Cluster configuration
HELM_KCP_PORT=38443                # KCP API server port
HELM_EAST_PORT=38444               # East cluster port  
HELM_WEST_PORT=38445               # West cluster port

# Helm configuration
HELM_TIMEOUT=10m                   # Installation timeout
HELM_WAIT=true                     # Wait for readiness
HELM_ATOMIC=true                   # Atomic installations
```

### Production Values Files
Create environment-specific values:

```yaml
# production-values.yaml
kcp:
  server:
    replicas: 3
    resources:
      requests:
        memory: "2Gi"
        cpu: "1000m"
      limits:
        memory: "4Gi"
        cpu: "2000m"

persistence:
  enabled: true
  storageClass: "fast-ssd"
  size: "100Gi"

monitoring:
  enabled: true
  serviceMonitor:
    enabled: true

security:
  podSecurityPolicy:
    enabled: true
  networkPolicy:
    enabled: true
```

## 📊 Monitoring and Observability

### Helm Release Monitoring
```bash
# Check release status
helm status kcp-tmc
helm status east-syncer

# View release history
helm history kcp-tmc
helm history east-workload

# Get release values
helm get values kcp-tmc
helm get values east-syncer
```

### Resource Monitoring
```bash
# Monitor resource usage
kubectl --context kind-helm-kcp top pods
kubectl --context kind-helm-east top pods

# Check health endpoints
kubectl port-forward svc/kcp-server 8080:8080
curl http://localhost:8080/healthz

# View metrics
kubectl port-forward svc/kcp-server 8081:8081
curl http://localhost:8081/metrics
```

## 🎯 Key Learning Points

### Production Deployment Patterns
1. **Helm Best Practices**: Production-ready chart structure and templates
2. **Configuration Management**: Values-driven, environment-specific configs
3. **Resource Management**: Proper limits, requests, and health checks
4. **Operational Readiness**: Upgrade, rollback, and scaling patterns

### Enterprise Integration
1. **GitOps Ready**: Charts and values in version control
2. **CI/CD Compatible**: Automated deployment pipelines
3. **Multi-Environment**: Development, staging, production configs
4. **Observability**: Built-in monitoring and alerting

### TMC Production Considerations
1. **High Availability**: Multi-replica KCP deployments
2. **Security**: RBAC, PSP, and network policies
3. **Persistence**: Proper storage for KCP state
4. **Networking**: Load balancers and ingress configuration

## 🔍 Troubleshooting

### Common Issues

**Helm chart validation fails**:
```bash
# Check chart syntax
helm lint ../../charts/kcp-tmc
helm lint ../../charts/kcp-syncer

# Debug template rendering
helm template kcp-tmc ../../charts/kcp-tmc -f manifests/kcp-tmc-values.yaml
```

**Release installation fails**:
```bash
# Check release status
helm status kcp-tmc

# View installation logs
kubectl logs -l app.kubernetes.io/name=kcp-server

# Debug with dry-run
helm install kcp-tmc ../../charts/kcp-tmc -f manifests/kcp-tmc-values.yaml --dry-run --debug
```

**Resource limits causing issues**:
```bash
# Check resource usage
kubectl top pods
kubectl describe pod <pod-name>

# Adjust values and upgrade
helm upgrade kcp-tmc ../../charts/kcp-tmc --set kcp.server.resources.limits.memory=2Gi
```

### Debug Mode
```bash
# Full debug output with Helm operations
DEMO_DEBUG=true ./run-demo.sh

# This shows:
# - All helm commands with full output
# - Template rendering details
# - Resource creation and status
# - Configuration validation steps
```

## 🧹 Cleanup

### Automatic Cleanup
```bash
# Demo cleans up automatically unless specified
./run-demo.sh

# Keep everything for exploration
DEMO_SKIP_CLEANUP=true ./run-demo.sh

# Manual cleanup anytime
./cleanup.sh
```

### Selective Cleanup
```bash
# Remove only Helm releases, keep clusters
./cleanup.sh --demo-only

# Remove everything
./cleanup.sh --full

# Force cleanup ignoring errors
./cleanup.sh --force
```

### Manual Helm Cleanup
```bash
# Uninstall specific releases
helm uninstall kcp-tmc
helm uninstall east-syncer
helm uninstall west-syncer
helm uninstall east-workload
helm uninstall west-workload

# List all releases
helm list --all-namespaces
```

## 🎓 Learning Outcomes

After completing this demo, you'll understand:

### Production Helm Patterns
- How to structure production-ready TMC Helm charts
- Values-driven configuration management
- Template best practices for Kubernetes deployments
- Operational patterns for Helm-based applications

### Enterprise Operations
- Helm upgrade and rollback strategies
- Multi-environment deployment patterns
- GitOps integration approaches
- Monitoring and observability setup

### TMC Production Deployment
- Production-grade TMC architecture
- Resource planning and scaling considerations
- Security and compliance requirements
- Operational procedures and best practices

## 🚀 Next Steps

After completing this demo:

1. **Customize**: Modify values files for your environment
2. **Integrate**: Set up GitOps workflows (ArgoCD, Flux)
3. **Monitor**: Add Prometheus/Grafana monitoring
4. **Secure**: Implement proper TLS and RBAC
5. **Scale**: Try the [Production Setup](../production-setup/) demo

## 📚 Additional Resources

- [Helm Chart Development](../../charts/README.md)
- [Production Deployment Guide](../../docs/content/deployment/production.md)
- [TMC Configuration Reference](../../docs/content/configuration/tmc.md)
- [Operational Procedures](../../docs/content/operations/README.md)