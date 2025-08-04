# TMC Production Setup Demo

This demo showcases enterprise-grade multi-cluster TMC deployment with production features including high availability, security hardening, monitoring, and operational best practices.

## 🎯 What You'll Learn

- Enterprise multi-cluster architecture design
- Production security and compliance features
- High availability and disaster recovery patterns
- Complete monitoring and observability stack
- Operational procedures and best practices
- Multi-zone resilience and failover capabilities

## 📋 Prerequisites

- **Docker** 20.10+ (running)
- **kubectl** 1.26+
- **kind** 0.17+
- **helm** 3.8+
- **bash** (for running scripts)

**System Requirements**:
- 16GB+ available RAM (recommended for full simulation)
- 30GB+ free disk space
- Internet connection for pulling images

## 🎬 Demo Scenario

**The Challenge**: You need to deploy TMC in a production environment with:
- High availability across multiple zones
- Enterprise security and compliance requirements
- Complete monitoring and observability
- Disaster recovery and business continuity
- Operational procedures and automation

**Production Solution**: 
- Multi-node clusters with zone distribution
- HA KCP deployment with multiple replicas
- Security hardening with PSP, RBAC, NetworkPolicies
- Complete monitoring stack (Prometheus, Grafana)
- Production-grade resource management
- Automated health checks and recovery

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
production-setup/
├── README.md                    # This file
├── run-demo.sh                 # Main demo script
├── cleanup.sh                  # Cleanup script
├── validate-demo.sh            # Validation script
├── configs/                    # Production cluster configurations
│   ├── kcp-cluster-config.yaml      # HA KCP cluster
│   ├── east-cluster-config.yaml     # Multi-zone east
│   ├── west-cluster-config.yaml     # Multi-zone west
│   └── monitor-cluster-config.yaml  # Monitoring cluster
├── manifests/                  # Production manifests
│   ├── production-kcp.yaml          # HA KCP deployment
│   ├── east-production-syncer.yaml  # Production syncer
│   ├── west-production-syncer.yaml  # Production syncer
│   ├── east-production-workload.yaml # Secure workload
│   ├── west-production-workload.yaml # Secure workload
│   ├── prometheus.yaml              # Monitoring stack
│   └── grafana.yaml                 # Dashboard stack
├── monitoring/                 # Monitoring configurations
│   └── prometheus-config.yaml      # Metrics collection config
├── kubeconfigs/             # Generated kubeconfig files
└── logs/                    # Demo execution logs
```

## 🔄 Demo Flow

### Step 1: Production Cluster Setup
- Creates 4 clusters: HA KCP, multi-zone east/west, dedicated monitoring
- Configures production-grade networking and storage
- Sets up proper node labeling and zone distribution

### Step 2: Monitoring Stack Installation
- Deploys Prometheus for metrics collection
- Installs Grafana for visualization and dashboards
- Configures scraping for all TMC components

### Step 3: HA KCP Deployment
- Deploys KCP with multiple replicas and anti-affinity
- Configures proper resource limits and health checks
- Sets up audit logging and security contexts

### Step 4: Production Syncer Installation
- Installs HA syncers with multiple replicas
- Configures security policies and network isolation
- Sets up monitoring and health endpoints

### Step 5: Secure Workload Deployment
- Deploys workloads with security hardening
- Applies Pod Disruption Budgets and anti-affinity
- Configures NetworkPolicies and RBAC

### Step 6: Production Validation
- Validates HA and failover capabilities
- Tests monitoring and alerting systems
- Demonstrates operational procedures

## 🎮 Interactive Features

### Production Dashboard
```bash
=== TMC Production Environment Status ===
┌─────────────────┬─────────────┬─────────┬─────────────┬──────────────┐
│ Cluster         │ Nodes       │ Status  │ Workloads   │ Health       │
├─────────────────┼─────────────┼─────────┼─────────────┼──────────────┤
│ KCP (HA)        │ 3 nodes     │ Healthy │ 2 replicas  │ ✅ All zones │
│ East (Multi-AZ) │ 3 nodes     │ Healthy │ 3 replicas  │ ✅ All zones │
│ West (Multi-AZ) │ 3 nodes     │ Healthy │ 3 replicas  │ ✅ All zones │
│ Monitor         │ 1 node      │ Healthy │ 2 services  │ ✅ Ready     │
└─────────────────┴─────────────┴─────────┴─────────────┴──────────────┘

🔒 Security: PSP ✅ | RBAC ✅ | NetworkPolicy ✅ | SecurityContext ✅
📊 Monitoring: Prometheus ✅ | Grafana ✅ | Metrics ✅ | Health ✅
🔄 HA: Multi-replica ✅ | Anti-affinity ✅ | PDB ✅ | Zone distribution ✅
```

### Real-Time Monitoring Access
```bash
=== Production Monitoring Endpoints ===
📊 Prometheus: http://localhost:9091
  • KCP metrics collection
  • Syncer health monitoring  
  • Resource usage tracking
  • Custom TMC metrics

📈 Grafana: http://localhost:3000 (admin/admin)
  • Pre-configured TMC dashboards
  • Real-time cluster health
  • Resource utilization graphs
  • Alert rule management

💚 Health Endpoints:
  • KCP Health: :8081/healthz
  • Syncer Health: :8081/healthz  
  • Application Health: :80/health
```

## 🧪 What the Demo Shows

### 1. High Availability Architecture
```yaml
# Multi-replica KCP with anti-affinity
apiVersion: apps/v1
kind: Deployment
metadata:
  name: kcp-server
spec:
  replicas: 2
  template:
    spec:
      affinity:
        podAntiAffinity:
          preferredDuringSchedulingIgnoredDuringExecution:
          - weight: 100
            podAffinityTerm:
              labelSelector:
                matchLabels:
                  app: kcp-server
              topologyKey: kubernetes.io/hostname
```

### 2. Security Hardening
```yaml
# Production security context
securityContext:
  allowPrivilegeEscalation: false
  runAsNonRoot: true
  runAsUser: 1000
  capabilities:
    drop:
    - ALL

# Network policy isolation
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: production-netpol
spec:
  podSelector:
    matchLabels:
      app: production-app
  policyTypes:
  - Ingress
  - Egress
```

### 3. Production Resource Management
```yaml
# Proper resource allocation
resources:
  requests:
    memory: "1Gi"
    cpu: "500m"
  limits:
    memory: "2Gi"
    cpu: "1000m"

# Pod Disruption Budget
apiVersion: policy/v1
kind: PodDisruptionBudget
metadata:
  name: kcp-server-pdb
spec:
  minAvailable: 1
  selector:
    matchLabels:
      app: kcp-server
```

### 4. Monitoring Integration
```yaml
# Prometheus metrics annotation
annotations:
  prometheus.io/scrape: "true"
  prometheus.io/port: "8080"
  prometheus.io/path: "/metrics"

# Health check configuration
livenessProbe:
  httpGet:
    path: /healthz
    port: 8081
  initialDelaySeconds: 30
  periodSeconds: 10
```

## 🔧 Configuration Options

### Environment Variables
```bash
# Demo behavior
DEMO_DEBUG=true                    # Enable debug output
DEMO_SKIP_CLEANUP=true             # Keep resources after demo
DEMO_PAUSE_STEPS=false             # Run without pauses

# Cluster configuration
PROD_KCP_PORT=39443                # KCP API server port
PROD_EAST_PORT=39444               # East cluster port  
PROD_WEST_PORT=39445               # West cluster port
PROD_MONITOR_PORT=39446            # Monitor cluster port

# Production settings
PROD_HA_REPLICAS=2                 # Number of KCP replicas
PROD_SYNCER_REPLICAS=2             # Number of syncer replicas
PROD_WORKLOAD_REPLICAS=3           # Number of workload replicas
```

### Production Cluster Sizing
```yaml
# Resource recommendations
kcp:
  server:
    replicas: 2-3                  # HA deployment
    resources:
      requests:
        memory: "2Gi"
        cpu: "1000m"
      limits:
        memory: "4Gi"
        cpu: "2000m"

syncers:
  replicas: 2                      # Per cluster
  resources:
    requests:
      memory: "512Mi" 
      cpu: "200m"
    limits:
      memory: "1Gi"
      cpu: "500m"
```

## 📊 Monitoring and Observability

### Prometheus Metrics
```bash
# Key TMC metrics collected
kcp_api_requests_total              # API request count
kcp_syncer_sync_duration_seconds    # Sync operation latency
kcp_workspaces_total                # Number of workspaces
kcp_sync_targets_total              # Number of sync targets
kcp_resource_sync_errors_total      # Sync error count

# Custom business metrics
tmcapp_requests_total               # Application requests
tmcapp_response_time_seconds        # Response latency
tmcapp_active_connections           # Active connections
```

### Grafana Dashboards
```bash
# Pre-configured dashboards
• TMC Cluster Overview              # High-level cluster health
• KCP Server Metrics               # API server performance
• Syncer Performance               # Cross-cluster sync metrics
• Resource Utilization             # CPU, memory, storage
• Network Traffic                  # Inter-cluster communication
• Security Events                  # Policy violations, access
```

### Alerting Rules
```yaml
# Example production alerts
groups:
- name: tmc.rules
  rules:
  - alert: KCPServerDown
    expr: up{job="kcp-server"} == 0
    for: 1m
    labels:
      severity: critical
    annotations:
      summary: "KCP server is down"
      
  - alert: SyncerHighLatency
    expr: kcp_syncer_sync_duration_seconds > 10
    for: 5m
    labels:
      severity: warning
    annotations:
      summary: "Syncer experiencing high latency"
```

## 🎯 Key Learning Points

### Production Architecture Patterns
1. **High Availability**: Multi-replica, multi-zone deployments
2. **Security First**: Defense in depth with multiple security layers
3. **Observability**: Complete monitoring and alerting stack
4. **Operational Excellence**: Automation and best practices

### Enterprise Features
1. **Compliance Ready**: Security policies and audit logging
2. **Disaster Recovery**: Multi-region failover capabilities
3. **Performance**: Resource optimization and scaling patterns
4. **Maintainability**: Operational procedures and automation

### TMC Production Considerations
1. **Scale Planning**: Resource allocation and growth patterns
2. **Security Posture**: Threat modeling and defense strategies
3. **Operational Model**: Day-2 operations and maintenance
4. **Business Continuity**: Backup, recovery, and failover procedures

## 🔍 Troubleshooting

### Common Production Issues

**KCP HA failover not working**:
```bash
# Check pod anti-affinity
kubectl describe pod -l app=kcp-server

# Verify PDB configuration
kubectl get pdb kcp-server-pdb -o yaml

# Test failover by deleting one replica
kubectl delete pod -l app=kcp-server --force --grace-period=0
```

**Monitoring not collecting metrics**:
```bash
# Check Prometheus targets
kubectl port-forward svc/prometheus 9090:9090
# Visit http://localhost:9090/targets

# Verify metrics endpoints
kubectl port-forward svc/kcp-server 8080:8080
curl http://localhost:8080/metrics
```

**Security policies blocking workloads**:
```bash
# Check NetworkPolicy rules
kubectl get networkpolicy -A

# Verify RBAC permissions
kubectl auth can-i --list --as=system:serviceaccount:production-workloads:production-app

# Review security context denials
kubectl describe pod <pod-name>
```

### Production Health Checks
```bash
# Comprehensive health validation
./validate-demo.sh --production-check

# This verifies:
# - All components are HA and healthy
# - Security policies are enforced
# - Monitoring is collecting metrics
# - Network connectivity is working
# - Resource usage is within limits
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
# Remove only workloads, keep infrastructure
./cleanup.sh --demo-only

# Remove everything
./cleanup.sh --full

# Force cleanup ignoring errors
./cleanup.sh --force
```

### Production Cleanup Procedures
```bash
# Graceful production shutdown
1. Drain workloads from clusters
2. Scale down syncers gracefully
3. Backup KCP state and configuration
4. Remove clusters in dependency order
5. Verify complete cleanup
```

## 🎓 Learning Outcomes

After completing this demo, you'll understand:

### Production Architecture
- How to design HA multi-cluster TMC deployments
- Security hardening and compliance requirements
- Resource planning and capacity management
- Disaster recovery and business continuity

### Operational Excellence
- Monitoring and observability best practices
- Health checks and automated recovery
- Incident response and troubleshooting
- Change management and upgrade procedures

### Enterprise Integration
- Security and compliance frameworks
- Monitoring and alerting strategies
- Backup and disaster recovery procedures
- Performance optimization and scaling

## 🚀 Next Steps

After completing this demo:

1. **Production Planning**: Design your real production architecture
2. **Security Review**: Conduct security assessment and threat modeling
3. **Monitoring Setup**: Implement comprehensive observability
4. **DR Planning**: Design disaster recovery procedures
5. **Operations**: Establish operational procedures and runbooks

## 📚 Additional Resources

- [Production Architecture Guide](../../docs/production/architecture.md)
- [Security Best Practices](../../docs/security/hardening.md)
- [Monitoring and Observability](../../docs/operations/monitoring.md)
- [Disaster Recovery Procedures](../../docs/operations/disaster-recovery.md)
- [Operational Runbooks](../../docs/operations/runbooks/)