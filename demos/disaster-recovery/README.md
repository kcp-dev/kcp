# TMC Disaster Recovery Demo

This demo showcases TMC's automatic failover capabilities between multiple regions when one becomes unavailable, demonstrating how TMC ensures high availability and regional resilience for critical applications.

## 🎯 What You'll Learn

- **Multi-region deployment patterns** with TMC transparency
- **Automatic health monitoring** across distributed clusters
- **Real-time failover** when regions become unavailable
- **Traffic redirection** to healthy regions without user intervention
- **Automatic recovery** and load rebalancing when regions come back online
- **Regional isolation** with global coordination

## 📋 Prerequisites

- **Docker** 20.10+ (running)
- **kubectl** 1.26+
- **kind** 0.17+
- **bash** (for running scripts)

**System Requirements**:
- 8GB+ available RAM
- 20GB+ free disk space
- Internet connection for pulling images

## 🎬 Demo Scenario

**The Challenge**: You have a critical web application that must remain available even when entire regions fail. Users expect:
- Zero downtime during regional outages
- Automatic failover without manual intervention
- Seamless recovery when regions come back online
- Consistent performance regardless of regional health

**TMC Solution**: 
- Web application deployed across East and West regions
- Global load balancer distributes traffic between healthy regions
- Health monitors continuously check regional status
- Failover controller automatically redirects traffic during failures
- TMC ensures transparent synchronization and coordination

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
disaster-recovery/
├── README.md                    # This file
├── run-demo.sh                 # Main demo script
├── cleanup.sh                  # Cleanup script
├── validate-demo.sh            # Validation script
├── configs/                    # Cluster configurations
│   ├── kcp-host-config.yaml
│   ├── east-cluster-config.yaml
│   └── west-cluster-config.yaml
├── manifests/                  # Kubernetes manifests
│   ├── webapp-east.yaml        # East region web application
│   ├── webapp-west.yaml        # West region web application
│   ├── global-loadbalancer.yaml # Global traffic manager
│   ├── east-syncer.yaml        # East TMC syncer
│   ├── west-syncer.yaml        # West TMC syncer
│   ├── health-monitor-east.yaml # East health monitor
│   ├── health-monitor-west.yaml # West health monitor
│   └── failover-controller.yaml # Global failover controller
├── scripts/                    # Helper scripts
│   ├── show-status.sh          # Display current system status
│   ├── monitor-failover.sh     # Real-time failover monitoring
│   ├── simulate-failure.sh     # Simulate regional failures
│   └── simulate-recovery.sh    # Simulate regional recovery
├── kubeconfigs/               # Generated kubeconfig files
└── logs/                      # Demo execution logs
```

## 🔄 Demo Flow

### Step 1: Multi-Region Infrastructure Setup
- Creates KCP host cluster for global coordination
- Deploys East region cluster (us-east-1)
- Deploys West region cluster (us-west-2)
- Establishes TMC syncers for cross-cluster communication

### Step 2: Application Deployment Across Regions
- Deploys identical web applications to both regions
- Sets up regional health monitoring
- Configures global load balancer for traffic distribution
- Validates application accessibility from all regions

### Step 3: Health Monitoring and Failover Setup
- Deploys health monitors in each region
- Sets up failover controller in global cluster
- Configures automatic failure detection thresholds
- Establishes traffic routing policies

### Step 4: Disaster Simulation and Failover
- Simulates East region failure (infrastructure or application)
- Demonstrates automatic detection of regional failure
- Shows real-time traffic redirection to healthy West region
- Validates that users experience no service disruption

### Step 5: Recovery and Rebalancing
- Simulates East region recovery
- Shows automatic detection of regional recovery
- Demonstrates traffic rebalancing between regions
- Validates return to active-active load distribution

### Step 6: Monitoring and Operations
- Real-time dashboard for regional health
- Interactive failure simulation tools
- Recovery management capabilities
- System health validation

## 🎮 Interactive Features

### Real-Time Failover Dashboard

**Use the dedicated monitoring script for the best experience:**
```bash
./scripts/monitor-failover.sh
```

This provides a live dashboard showing:
```bash
===============================================================
🌍 TMC Disaster Recovery Monitor
===============================================================
Last updated: Wed Aug  3 10:15:22 PDT 2025 | Press Ctrl+C to stop

Regional Status
┌─────────────────┬─────────────┬─────────────┬─────────────────┬─────────────────┐
│ Region          │ Cluster     │ Nodes       │ Application     │ TMC Syncer      │
├─────────────────┼─────────────┼─────────────┼─────────────────┼─────────────────┤
│ Global          │ kcp         │ 2 nodes     │ N/A             │ ✅ Controller   │
│ East            │ us-east-1   │ 2 nodes     │ ❌ Failed       │ ✅ Connected    │
│ West            │ us-west-2   │ 2 nodes     │ ✅ Active       │ ✅ Connected    │
└─────────────────┴─────────────┴─────────────┴─────────────────┴─────────────────┘

Traffic Distribution & Failover Status
┌─────────────────┬─────────────┬─────────────┬─────────────────┐
│ Region          │ Status      │ Traffic     │ Replicas        │
├─────────────────┼─────────────┼─────────────┼─────────────────┤
│ East (us-east-1)│ FAILED      │ 0%          │ 0/2 pods        │
│ West (us-west-2)│ HEALTHY     │ 100%        │ 2/2 pods        │
└─────────────────┴─────────────┴─────────────┴─────────────────┘

Failover Status: ⚠️ Failover: West Only

🔄 Updates every 5s • Press 'h' for help • Ctrl+C to stop
```

### Interactive Failure Simulation

The monitoring dashboard supports interactive commands:
- **Press 's'** - Simulate East region failure
- **Press 'r'** - Recover East region
- **Press 'w'** - Simulate West region failure  
- **Press 'e'** - Recover West region
- **Press 'h'** - Show help menu

### Manual Failure Testing

You can also manually test failures:
```bash
# Simulate regional failures
./scripts/simulate-failure.sh east   # Fail East region
./scripts/simulate-failure.sh west   # Fail West region

# Test recovery
./scripts/simulate-recovery.sh east  # Recover East region
./scripts/simulate-recovery.sh west  # Recover West region

# Check current status
./scripts/show-status.sh
```

## 🧪 What the Demo Shows

### 1. Multi-Region Web Application
```yaml
# East Region Application
apiVersion: apps/v1
kind: Deployment
metadata:
  name: webapp-east
  labels:
    region: east
    demo: disaster-recovery
spec:
  replicas: 2
  template:
    spec:
      containers:
      - name: webapp
        image: nginx:alpine
        env:
        - name: REGION
          value: "us-east-1"
        # Health checks and region-specific content
```

### 2. Global Load Balancer with Failover Logic
```yaml
# Global Load Balancer (deployed to KCP)
apiVersion: apps/v1
kind: Deployment  
metadata:
  name: global-loadbalancer
spec:
  # Monitors regional health and routes traffic
  # Automatically fails over during regional outages
  # Rebalances traffic during recovery
```

### 3. Automatic Health Monitoring
```yaml
# Regional Health Monitor
apiVersion: apps/v1
kind: Deployment
metadata:
  name: health-monitor-east
spec:
  # Continuously monitors regional application health
  # Reports status to global failover controller
  # Triggers failover events when thresholds exceeded
```

### 4. TMC Cross-Cluster Synchronization
- **Resource Visibility**: Applications visible across all clusters
- **Status Propagation**: Health status automatically synchronized
- **Configuration Consistency**: Load balancer rules updated globally
- **Event Coordination**: Failover events visible across regions

## 🔧 Configuration Options

### Environment Variables
```bash
# Demo behavior
DEMO_DEBUG=true                    # Enable debug output
DEMO_SKIP_CLEANUP=true             # Keep resources after demo
DEMO_PAUSE_STEPS=false             # Run without pauses

# Cluster configuration
DR_KCP_PORT=38443                  # KCP API server port
DR_EAST_PORT=38444                 # East cluster port
DR_WEST_PORT=38445                 # West cluster port

# Failover settings
FAILOVER_THRESHOLD=3               # Failures before triggering failover
HEALTH_CHECK_INTERVAL=20s          # How often to check health
RECOVERY_TIMEOUT=300s              # Maximum time to wait for recovery
```

### Custom Regional Configuration
You can modify the cluster configurations to simulate different regions:
```yaml
# configs/east-cluster-config.yaml
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
name: dr-east
nodes:
- role: control-plane
  kubeadmConfigPatches:
  - |
    kind: InitConfiguration
    nodeRegistration:
      kubeletExtraArgs:
        node-labels: "region=us-east-1,zone=east-1a,disaster-recovery=enabled"
```

## 📊 Monitoring and Observability

### Real-Time System Status
```bash
# Comprehensive status overview
./scripts/show-status.sh

# Continuous monitoring dashboard
./scripts/monitor-failover.sh

# Validate all components
./validate-demo.sh --check-all
```

### Failure Scenario Testing
```bash
# Test East region failure
./scripts/simulate-failure.sh east
./scripts/monitor-failover.sh  # Watch the failover happen

# Test recovery
./scripts/simulate-recovery.sh east
./scripts/monitor-failover.sh  # Watch traffic rebalance
```

### Health Check Endpoints
The demo applications include health check endpoints:
```bash
# Check application health directly
kubectl --context kind-dr-east port-forward svc/webapp-east-svc 8080:80 &
curl http://localhost:8080/health  # Should return "OK"
curl http://localhost:8080/ready   # Should return "READY"
```

## 🎯 Key Learning Points

### TMC Disaster Recovery Patterns
1. **Global Coordination**: TMC enables global control plane with regional execution
2. **Transparent Failover**: Applications and users don't need to know about failures
3. **Automatic Recovery**: Traffic automatically rebalances when regions recover
4. **Regional Isolation**: Failures in one region don't affect others

### Production Implications
1. **Regional Distribution**: Deploy applications across geographically diverse regions
2. **Health Monitoring**: Continuous monitoring is essential for reliable failover
3. **Traffic Management**: Global load balancing with health-based routing
4. **Recovery Planning**: Automated recovery reduces mean time to repair (MTTR)

### Advanced Scenarios
1. **Multi-Application Failover**: Coordinated failover of multiple dependent services
2. **Data Consistency**: Ensuring data synchronization during regional failures
3. **Gradual Recovery**: Phased traffic restoration during region recovery
4. **Capacity Planning**: Ensuring remaining regions can handle full load

## 🔍 Troubleshooting

### Common Issues

**Cluster not accessible**:
```bash
# Check if kind clusters are running
kind get clusters
docker ps  # Should show kind containers

# Recreate cluster if needed
kind delete cluster --name dr-east
kind create cluster --name dr-east --config configs/east-cluster-config.yaml
```

**Application not failing over**:
```bash
# Check failover controller logs
kubectl --context kind-dr-kcp logs deployment/failover-controller

# Verify health monitor connectivity
kubectl --context kind-dr-east logs deployment/health-monitor-east
kubectl --context kind-dr-west logs deployment/health-monitor-west
```

**TMC synchronization issues**:
```bash
# Check syncer status
kubectl --context kind-dr-east get deployment kcp-syncer
kubectl --context kind-dr-west get deployment kcp-syncer

# Verify syncer logs
kubectl --context kind-dr-east logs deployment/kcp-syncer
kubectl --context kind-dr-west logs deployment/kcp-syncer
```

**Recovery not working**:
```bash
# Check if deployments are scaling up
kubectl --context kind-dr-east get deployment webapp-east -w

# Force manual recovery if needed
kubectl --context kind-dr-east scale deployment/webapp-east --replicas=2
kubectl --context kind-dr-east wait --for=condition=available deployment/webapp-east
```

### Debug Mode
```bash
# Run with full debug output
DEMO_DEBUG=true ./run-demo.sh

# This shows:
# - All kubectl commands with contexts
# - Cluster creation and configuration steps
# - Application deployment progress
# - Health monitoring setup
# - Failover controller configuration
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
# Remove only demo resources, keep clusters
./cleanup.sh --demo-only

# Remove everything including clusters
./cleanup.sh --full

# Force cleanup ignoring errors
./cleanup.sh --force
```

## 🎓 Learning Outcomes

After completing this demo, you'll understand:

### Advanced TMC Capabilities
- How TMC enables transparent multi-region deployments
- The role of health monitoring in automatic failover
- Cross-cluster coordination for disaster recovery
- Regional isolation with global state management

### Practical Disaster Recovery Patterns
- Multi-region application deployment strategies
- Health monitoring and failure detection
- Automatic traffic routing and failover
- Recovery and rebalancing procedures

### Production Considerations
- Regional placement and latency optimization
- Capacity planning for failover scenarios
- Monitoring and alerting for disaster recovery
- Testing and validation of failover procedures

## 🚀 Next Steps

After completing this demo:

1. **Experiment**: Try different failure scenarios and recovery patterns
2. **Scale**: Add more regions and test complex failover scenarios
3. **Extend**: Add database failover and data replication
4. **Production**: Implement similar patterns in your production environment
5. **Advanced**: Try the [Multi-Tenant Demo](../multi-tenant/) or [Policy Enforcement Demo](../policy-enforcement/)

## 📚 Additional Resources

- [TMC Disaster Recovery Architecture](../../docs/content/developers/tmc/disaster-recovery.md)
- [Multi-Cluster Health Monitoring](../../docs/content/developers/tmc/health-monitoring.md)
- [Cross-Cluster Load Balancing](../../docs/content/developers/tmc/load-balancing.md)
- [Production Deployment Patterns](../helm-deployment/)
- [TMC API Reference](../../docs/content/developers/tmc/README.md)