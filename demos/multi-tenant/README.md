# TMC Multi-Tenant Demo

This demo showcases **TMC (Transparent Multi-Cluster) multi-tenant capabilities** with isolated tenant workspaces across multiple Kubernetes clusters. It demonstrates how to achieve secure, scalable multi-tenancy with both shared and dedicated cluster deployments.

## 🎯 What This Demo Shows

- **Multi-Tenant Isolation**: Complete tenant separation with network, storage, and compute boundaries
- **Flexible Deployment Models**: Both shared multi-tenant and dedicated isolated clusters
- **Resource Management**: Per-tenant quotas, limits, and fair resource sharing
- **Security & Compliance**: RBAC, network policies, and enterprise-grade isolation
- **Cross-Cluster Coordination**: TMC-powered tenant management across clusters
- **Operational Excellence**: Comprehensive monitoring, reporting, and lifecycle management

## 🏗️ Architecture Overview

```
┌─────────────────────┐    ┌─────────────────────┐    ┌─────────────────────┐
│   KCP Host Cluster  │    │ Shared Multi-Tenant │    │ Isolated Enterprise │
│                     │    │     Cluster         │    │     Cluster         │
│ ┌─────────────────┐ │    │ ┌─────────────────┐ │    │ ┌─────────────────┐ │
│ │ Tenant Manager  │ │◄──►│ │ acme-corp       │ │    │ │   enterprise    │ │
│ │   Controller    │ │    │ │ beta-inc        │ │    │ │   (dedicated)   │ │
│ │                 │ │    │ │ gamma-ltd       │ │    │ │                 │ │
│ └─────────────────┘ │    │ └─────────────────┘ │    │ └─────────────────┘ │
│                     │    │                     │    │                     │
│ • TMC Coordination  │    │ • Resource Sharing  │    │ • Full Isolation    │
│ • Policy Engine     │    │ • Network Isolation │    │ • Enhanced Security │
│ • Resource Tracking │    │ • RBAC Boundaries   │    │ • Compliance Ready  │
└─────────────────────┘    └─────────────────────┘    └─────────────────────┘
```

## 🚀 Quick Start

### Prerequisites

- Docker and Kind installed
- kubectl configured
- At least 8GB RAM available
- Ports 41443, 41444, 41445 available

### Run the Demo

```bash
# Clone and navigate to demo
cd demos/multi-tenant

# Run the complete demo
./run-demo.sh

# Or run with environment options
DEMO_PAUSE_STEPS=false ./run-demo.sh  # Run without pauses
DEMO_DEBUG=true ./run-demo.sh         # Enable debug output
```

### Monitor in Real-Time

```bash
# Live tenant monitoring dashboard
./scripts/monitor-tenants.sh

# Static tenant status report
./scripts/show-tenant-status.sh

# Generate comprehensive resource report
./scripts/tenant-resource-report.sh
```

## 📊 Demo Flow

### Phase 1: Infrastructure Setup
1. **Cluster Creation**: 3 Kind clusters (KCP host, shared, isolated)
2. **TMC Integration**: Tenant management system deployment
3. **Network Configuration**: Cross-cluster communication setup

### Phase 2: Tenant Provisioning
1. **Shared Tenants**: acme-corp, beta-inc, gamma-ltd on shared cluster
2. **Isolated Tenant**: enterprise on dedicated cluster
3. **Resource Allocation**: Quotas, limits, and RBAC configuration

### Phase 3: Application Deployment
1. **Tenant Applications**: Containerized web applications per tenant
2. **Service Configuration**: ClusterIP services for shared, LoadBalancer for isolated
3. **Health Monitoring**: Liveness and readiness probes

### Phase 4: Isolation Demonstration
1. **Network Isolation**: Cross-tenant communication blocking
2. **Resource Boundaries**: CPU, memory, and storage limits
3. **Security Validation**: RBAC and access control testing

### Phase 5: Management & Monitoring
1. **Live Monitoring**: Real-time resource usage and health
2. **Tenant Lifecycle**: Create, update, delete operations
3. **Compliance Reporting**: Security posture and resource utilization

## 🏢 Tenant Deployment Models

### Shared Multi-Tenant Model
- **Use Case**: Cost-effective for smaller tenants
- **Resource Sharing**: Efficient cluster utilization
- **Isolation Level**: Strong boundaries with network/RBAC policies
- **Scaling**: Horizontal scaling within shared resources

```yaml
# Example shared tenant configuration
apiVersion: v1
kind: ResourceQuota
metadata:
  name: acme-corp-quota
spec:
  hard:
    requests.cpu: "1000m"
    requests.memory: "1Gi"
    limits.cpu: "2000m"
    limits.memory: "2Gi"
    pods: "10"
```

### Isolated Enterprise Model
- **Use Case**: High-security, compliance-driven tenants
- **Resource Dedication**: Entire cluster per tenant
- **Isolation Level**: Complete physical separation
- **Scaling**: Vertical and horizontal scaling with dedicated resources

```yaml
# Example isolated tenant configuration
apiVersion: v1
kind: ResourceQuota
metadata:
  name: enterprise-quota
spec:
  hard:
    requests.cpu: "4000m"
    requests.memory: "4Gi"
    limits.cpu: "8000m"
    limits.memory: "8Gi"
    pods: "50"
```

## 🔒 Security & Isolation Features

### Network Isolation
- **Default Deny**: All cross-tenant traffic blocked by default
- **Explicit Allow**: DNS and essential services whitelisted
- **Environment-Specific**: Different policies per cluster tier

### RBAC Boundaries
- **Tenant Scoping**: Service accounts limited to tenant namespace
- **Resource Permissions**: Fine-grained access control
- **Admin Separation**: Tenant admins cannot access other tenants

### Storage Isolation
- **PVC Boundaries**: Persistent volumes scoped to tenants
- **Encryption**: At-rest encryption for enterprise tenants
- **Backup Policies**: Tenant-specific data protection

### Compute Isolation
- **Resource Quotas**: CPU, memory, and pod limits enforced
- **QoS Classes**: Guaranteed, Burstable, and BestEffort scheduling
- **Node Affinity**: Workload placement control

## 📈 Resource Management

### Quota Enforcement
```yaml
# Shared tenant quotas
Tenant: acme-corp
- CPU: 1.0 cores (requests), 2.0 cores (limits)
- Memory: 1Gi requests, 2Gi limits
- Storage: 10Gi total
- Pods: 10 maximum

# Enterprise tenant quotas  
Tenant: enterprise
- CPU: 4.0 cores (requests), 8.0 cores (limits)
- Memory: 4Gi requests, 8Gi limits
- Storage: 100Gi total
- Pods: 50 maximum
```

### Cost Optimization
- **Shared Resources**: 67% cost savings vs dedicated clusters
- **Right-Sizing**: Automatic resource recommendation
- **Usage Tracking**: Per-tenant cost allocation
- **Reserved Capacity**: Commitment-based pricing options

## 🛠️ Management Operations

### Create New Tenant
```bash
# Create shared tenant
./scripts/create-tenant.sh new-company shared

# Create isolated tenant
./scripts/create-tenant.sh enterprise-client isolated
```

### Delete Tenant
```bash
# Delete with confirmation
./scripts/delete-tenant.sh old-tenant

# Force delete without confirmation
./scripts/delete-tenant.sh old-tenant --force
```

### Resource Reporting
```bash
# Generate comprehensive report
./scripts/tenant-resource-report.sh

# Monitor real-time usage
./scripts/monitor-tenants.sh
```

## 📊 Monitoring & Observability

### Real-Time Dashboard
The monitoring dashboard provides live visibility into:
- **Resource Utilization**: CPU, memory, storage per tenant
- **Application Health**: Uptime, response times, error rates
- **Isolation Status**: Network, RBAC, storage boundaries
- **Security Events**: Violations, blocked access attempts
- **Capacity Planning**: Growth trends and recommendations

### Key Metrics
```
Tenant Performance Metrics:
├── Resource Usage
│   ├── CPU utilization and throttling
│   ├── Memory consumption and OOM events
│   └── Storage I/O and capacity
├── Application Health
│   ├── Pod availability and restart counts
│   ├── Service response times
│   └── Error rates and success ratios
└── Security Posture
    ├── Network policy violations
    ├── RBAC access attempts
    └── Compliance framework adherence
```

### Alerting Thresholds
- **High Resource Usage**: >80% of tenant quota
- **Application Errors**: >1% error rate sustained
- **Security Violations**: Any cross-tenant access attempt
- **Capacity Planning**: Projected quota exhaustion <30 days

## 🎛️ Interactive Features

### Tenant Lifecycle Simulation
```bash
# Simulate tenant onboarding
./scripts/create-tenant.sh demo-tenant shared
kubectl --context kind-tenant-shared get all -n tenant-demo-tenant

# Test tenant isolation
kubectl --context kind-tenant-shared exec -n tenant-demo-tenant -it <pod> -- nslookup tenant-acme-corp-webapp-service.tenant-acme-corp.svc.cluster.local

# Verify resource constraints
kubectl --context kind-tenant-shared describe resourcequota -n tenant-demo-tenant
```

### Cross-Tenant Access Testing
```bash
# Attempt cross-tenant network access (should fail)
kubectl --context kind-tenant-shared exec -n tenant-acme-corp -it <pod> -- curl tenant-beta-inc-webapp-service.tenant-beta-inc.svc.cluster.local

# Attempt cross-tenant resource access (should fail)
kubectl --context kind-tenant-shared --as=system:serviceaccount:tenant-acme-corp:acme-corp-service-account get pods -n tenant-beta-inc
```

## 🧪 Testing Scenarios

### Isolation Validation
1. **Network Boundaries**: Verify cross-tenant traffic blocking
2. **RBAC Enforcement**: Test cross-tenant resource access denial
3. **Resource Quotas**: Validate limit enforcement and fair sharing
4. **Storage Isolation**: Confirm PVC access restrictions

### Performance Testing
1. **Resource Contention**: High load on one tenant shouldn't affect others
2. **Scaling Behavior**: Tenant applications scale within their boundaries
3. **Network Performance**: Isolation doesn't impact legitimate traffic
4. **Storage I/O**: Per-tenant storage performance isolation

### Security Testing
1. **Privilege Escalation**: Attempt container breakout scenarios
2. **Service Discovery**: Cross-tenant service enumeration blocking
3. **Secret Access**: Tenant secret isolation validation
4. **API Server Access**: RBAC boundary enforcement

## 📚 Production Considerations

### Scaling Guidelines
- **Shared Clusters**: 20-50 tenants per cluster (depending on workload)
- **Resource Overhead**: ~10% for tenant management and isolation
- **Network Policies**: Scale linearly with tenant count
- **RBAC Objects**: Plan for N*M complexity (tenants × resources)

### Security Best Practices
- **Regular Audits**: Quarterly tenant isolation validation
- **Policy Updates**: Keep network and RBAC policies current
- **Vulnerability Scanning**: Per-tenant container image scanning
- **Compliance Monitoring**: Continuous framework adherence checking

### Operational Excellence
- **Automated Provisioning**: Self-service tenant creation
- **Cost Allocation**: Chargeback/showback implementation
- **Capacity Planning**: Predictive scaling based on trends
- **Disaster Recovery**: Cross-region tenant backup and restore

## 🔧 Troubleshooting

### Common Issues

#### Tenant Creation Failures
```bash
# Check cluster connectivity
kubectl --context kind-tenant-shared cluster-info

# Verify resource availability
kubectl --context kind-tenant-shared describe nodes

# Check for naming conflicts
kubectl --context kind-tenant-shared get namespaces | grep tenant-
```

#### Network Policy Issues
```bash
# List all network policies
kubectl --context kind-tenant-shared get networkpolicy --all-namespaces

# Test DNS resolution
kubectl --context kind-tenant-shared exec -n tenant-<name> -it <pod> -- nslookup kubernetes.default.svc.cluster.local

# Verify policy application
kubectl --context kind-tenant-shared describe networkpolicy -n tenant-<name>
```

#### Resource Quota Problems
```bash
# Check quota usage
kubectl --context kind-tenant-shared describe resourcequota -n tenant-<name>

# View limit ranges
kubectl --context kind-tenant-shared describe limitrange -n tenant-<name>

# Monitor resource consumption
kubectl --context kind-tenant-shared top pods -n tenant-<name>
```

### Debug Mode
Enable comprehensive logging for troubleshooting:
```bash
DEMO_DEBUG=true ./run-demo.sh
```

## 🧹 Cleanup

Remove all demo resources:
```bash
# Interactive cleanup (with confirmations)
./cleanup.sh

# Force cleanup (no confirmations)
./cleanup.sh --force
```

The cleanup script removes:
- All Kind clusters
- Docker containers and networks
- Temporary files and logs
- Kubeconfig files

Report files are preserved by default but can be optionally removed.

## 📖 Related Documentation

- [TMC Architecture Overview](../README.md)
- [Disaster Recovery Demo](../disaster-recovery/README.md)
- [GitOps Integration Demo](../gitops-integration/README.md)
- [Policy Enforcement Demo](../policy-enforcement/README.md)

## 🤝 Contributing

To extend this demo:

1. **Add Tenant Types**: Create new tenant templates in `tenants/`
2. **Extend Monitoring**: Add metrics to `scripts/monitor-tenants.sh`
3. **New Scenarios**: Add validation scripts to `scripts/`
4. **Documentation**: Update this README with new features

## 📝 License

This demo is part of the TMC project and follows the same licensing terms.

---

**Next Steps**: After exploring multi-tenancy, check out the [Policy Enforcement Demo](../policy-enforcement/README.md) to see how TMC enforces governance across your multi-tenant infrastructure.