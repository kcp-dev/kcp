# TMC Cross-Cluster Controller Demo

This demo showcases advanced TMC capabilities where a controller running on one cluster manages Custom Resources from multiple clusters, with status automatically synchronized back to the originating clusters.

## 🎯 What You'll Learn

- Advanced cross-cluster Custom Resource management
- Controller deployment patterns in TMC environments
- Custom Resource Definition (CRD) synchronization
- Bidirectional status propagation between clusters
- Real-time cross-cluster operations monitoring
- Status updates visible across all participating clusters

## 📋 Prerequisites

- **Docker** 20.10+ (running)
- **kubectl** 1.26+
- **kind** 0.17+
- **bash** (for running scripts)

**System Requirements**:
- 6GB+ available RAM
- 15GB+ free disk space
- Internet connection for pulling images

## 🎬 Demo Scenario

**The Challenge**: You need a global TaskQueue processing system where:
- Tasks can be submitted from any region (east/west clusters)
- A centralized controller processes all tasks
- Status updates are visible everywhere instantly
- The system remains resilient to regional failures

**TMC Solution**: 
- TaskQueue controller runs on west cluster
- TaskQueue CRDs synchronized to all clusters
- Users submit TaskQueues from their local clusters
- Controller processes tasks from all regions
- Status updates propagate back automatically

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
cross-cluster-controller/
├── README.md                    # This file
├── run-demo.sh                 # Main demo script
├── cleanup.sh                  # Cleanup script
├── validate-demo.sh            # Validation script
├── configs/                    # Cluster configurations
│   ├── kcp-host-config.yaml
│   ├── east-cluster-config.yaml
│   └── west-cluster-config.yaml
├── manifests/                  # Kubernetes manifests
│   ├── taskqueue-crd.yaml
│   ├── taskqueue-controller.yaml
│   ├── east-taskqueue.yaml
│   ├── west-taskqueue.yaml
│   └── global-taskqueue.yaml
├── scripts/                    # Helper scripts
│   ├── monitor-processing.sh   # Real-time TaskQueue monitoring
│   └── create-demo-taskqueues.sh # Create sample TaskQueues
├── kubeconfigs/             # Generated kubeconfig files
└── logs/                    # Demo execution logs
```

## 🔄 Demo Flow

### Step 1: Multi-Cluster Setup
- Creates KCP host + east/west clusters with unique naming
- Configures TMC syncers for cross-cluster communication
- Establishes secure connections between all clusters

### Step 2: CRD Synchronization
- Installs TaskQueue CRD on all clusters
- Validates CRD propagation via TMC
- Ensures consistent schema across clusters

### Step 3: Controller Deployment
- Deploys TaskQueue controller **only** to west cluster
- Controller watches TaskQueues from **all** clusters
- Demonstrates centralized processing architecture

### Step 4: Cross-Cluster Task Submission
- Creates TaskQueues on different clusters
- Shows automatic resource synchronization
- Validates cross-cluster visibility

### Step 5: Processing Demonstration
- Controller processes tasks from all clusters
- Status updates propagate back to origin clusters
- Real-time monitoring of cross-cluster operations

### Step 6: Status Verification
- Verifies status consistency across clusters
- Shows bidirectional synchronization
- Demonstrates TMC transparency

## 🎮 Interactive Features

### Real-Time Status Dashboard

**Use the dedicated monitoring script for the best experience:**
```bash
./scripts/monitor-processing.sh
```

This provides a live dashboard showing:
```bash
=== TMC Cross-Cluster Controller Monitor ===
┌─────────────────┬─────────────┬─────────────┬─────────────────┐
│ Cluster         │ Status      │ Nodes       │ Controller      │
├─────────────────┼─────────────┼─────────────┼─────────────────┤
│ KCP Host        │ ✅ Running  │ 1 nodes     │ None            │
│ East Cluster    │ ✅ Running  │ 1 nodes     │ None            │
│ West Cluster    │ ✅ Running  │ 1 nodes     │ ✅ Active       │
└─────────────────┴─────────────┴─────────────┴─────────────────┘

┌────────────────────┬──────────────┬─────────────┬─────────┬─────────────────┐
│ TaskQueue Name     │ Status       │ Origin      │ Tasks   │ Controller      │
├────────────────────┼──────────────┼─────────────┼─────────┼─────────────────┤
│ east-data          │ 🔄 Running   │ [from east] │ 3/5     │ 🎮 west         │
│ west-ml            │ 🔄 Running   │ [LOCAL]     │ 1/4     │ 🎮 west         │
│ global-monitor     │ ✅ Complete  │ [from kcp]  │ 4/4     │ 🎮 west         │
└────────────────────┴──────────────┴─────────────┴─────────┴─────────────────┘

🔄 Updates every 5s • Press 'h' for help • Ctrl+C to stop
```

### Cross-Cluster Verification
The demo shows the same TaskQueue visible and updated on all clusters:

**East Cluster View**:
```bash
$ kubectl get taskqueues
NAME                STATUS      TASKS    CONTROLLER
east-data          Running     3/5      west-controller
west-ml            Running     1/4      west-controller  # ← Synced from west
global-monitor     Complete    4/4      west-controller  # ← Synced from KCP
```

**West Cluster View**:
```bash
$ kubectl get taskqueues  
NAME                STATUS      TASKS    CONTROLLER
east-data          Running     3/5      west-controller  # ← Synced from east
west-ml            Running     1/4      west-controller
global-monitor     Complete    4/4      west-controller  # ← Synced from KCP
```

## 🧪 What the Demo Shows

### 1. Cross-Cluster CRD Management
```yaml
# TaskQueue created on east cluster
apiVersion: demo.tmc.io/v1
kind: TaskQueue
metadata:
  name: east-data-processing
  namespace: default
  labels:
    origin-cluster: east
spec:
  region: us-east-1
  priority: high
  tasks:
  - name: ingest-data
    command: "process --input=data.csv"
  - name: validate-data
    command: "validate --rules=business.yaml"

# Automatically visible and processable on west cluster
# Status updates propagated back to east cluster
```

### 2. Centralized Controller Architecture
```yaml
# Controller running ONLY on west cluster
apiVersion: apps/v1
kind: Deployment
metadata:
  name: taskqueue-controller
  namespace: default
spec:
  # ... controller that processes TaskQueues from ALL clusters
  # Updates status visible on ALL clusters
```

### 3. Bidirectional Status Synchronization
```bash
# Status update from west controller
kubectl patch taskqueue east-data-processing --type='merge' --patch='{
  "status": {
    "phase": "Processing",
    "completedTasks": 2,
    "processingCluster": "west-controller"
  }
}'

# Status automatically visible on east cluster
# Users on east see real-time updates from west controller
```

## 🔧 Configuration Options

### Environment Variables
```bash
# Demo behavior
DEMO_DEBUG=true                    # Enable debug output
DEMO_SKIP_CLEANUP=true             # Keep resources after demo
DEMO_PAUSE_STEPS=false             # Run without pauses

# Cluster configuration
CONTROLLER_KCP_PORT=37443          # KCP API server port
CONTROLLER_EAST_PORT=37444         # East cluster port  
CONTROLLER_WEST_PORT=37445         # West cluster port

# Controller settings
CONTROLLER_WORKERS=5               # Number of worker threads
CONTROLLER_SYNC_INTERVAL=10s       # How often to sync
CONTROLLER_PROCESSING_DELAY=2s     # Simulated processing time
```

### Custom TaskQueues
You can create your own TaskQueues:
```yaml
apiVersion: demo.tmc.io/v1
kind: TaskQueue
metadata:
  name: my-custom-queue
  namespace: default
spec:
  region: us-central-1
  priority: critical
  parallelism: 10
  tasks:
  - name: custom-task-1
    command: "my-processor --input=data"
    timeout: "5m"
  - name: custom-task-2
    command: "my-validator --rules=custom"
    timeout: "2m"
```

## 📊 Monitoring and Observability

### Interactive Monitoring Scripts

The demo includes powerful scripts to visualize cross-cluster operations:

#### 1. Create Demo TaskQueues
```bash
./scripts/create-demo-taskqueues.sh
```
Creates sample TaskQueues on different clusters:
- **east-data-processing**: Data pipeline tasks (created on East cluster)
- **west-ml-training**: ML model training tasks (created on West cluster)  
- **global-health-monitor**: Health check tasks (created on KCP cluster)

#### 2. Real-Time Monitoring Dashboard
```bash
./scripts/monitor-processing.sh
```
Provides live visualization of:
- Cross-cluster TaskQueue synchronization
- Controller processing activity
- Task completion progress
- TMC syncer health status

### Real-Time Monitoring
```bash
# Create sample TaskQueues for demonstration
./scripts/create-demo-taskqueues.sh

# Watch TaskQueue status across all clusters (RECOMMENDED)
./scripts/monitor-processing.sh

# Watch controller logs
kubectl --context kind-controller-west logs -f deployment/taskqueue-controller

# Monitor TMC syncer health
kubectl --context kind-controller-east logs -f deployment/kcp-syncer
kubectl --context kind-controller-west logs -f deployment/kcp-syncer
```

### Status Verification
```bash
# Compare TaskQueue status across clusters
./validate-demo.sh --check-status

# Verify controller functionality
./validate-demo.sh --check-controller

# Test cross-cluster synchronization
./validate-demo.sh --check-sync
```

## 🎯 Key Learning Points

### TMC Architectural Patterns
1. **Controller Placement**: Controllers can run anywhere, manage everywhere
2. **Resource Synchronization**: CRDs must exist on all participating clusters
3. **Status Propagation**: Updates are automatically bidirectional
4. **Transparency**: Users see consistent views regardless of location

### Production Implications
1. **Regional Controllers**: Place controllers near compute resources
2. **Global Coordination**: Centralized logic with distributed execution  
3. **Failure Resilience**: Regional failures don't affect other regions
4. **Scaling Patterns**: Add regions without changing controller logic

### Advanced Scenarios
1. **Multi-Controller**: Different controllers for different resource types
2. **Controller Failover**: Move controllers between regions dynamically
3. **Workload Placement**: Intelligent scheduling based on cluster capacity
4. **Policy Enforcement**: Global policies with local enforcement

## 🔍 Troubleshooting

### Common Issues

**CRD not syncing**:
```bash
# Check if CRD exists on all clusters
kubectl --context kind-controller-east get crd taskqueues.demo.tmc.io
kubectl --context kind-controller-west get crd taskqueues.demo.tmc.io

# Check syncer connectivity
kubectl --context kind-controller-east logs deployment/kcp-syncer
```

**Controller not processing cross-cluster resources**:
```bash
# Verify controller has access to all resources
kubectl --context kind-controller-west get taskqueues --all-namespaces

# Check controller logs for errors
kubectl --context kind-controller-west logs deployment/taskqueue-controller
```

**Status not synchronizing**:
```bash
# Check TMC syncer health
./validate-demo.sh --check-sync

# Verify network connectivity between clusters
docker network inspect kind
```

### Debug Mode
```bash
# Full debug output with command tracing
DEMO_DEBUG=true ./run-demo.sh

# This shows:
# - All kubectl commands with contexts
# - Resource creation and updates
# - TMC synchronization steps
# - Controller processing logic
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
# Remove only TaskQueues, keep clusters
./cleanup.sh --demo-only

# Remove everything
./cleanup.sh --full

# Force cleanup ignoring errors
./cleanup.sh --force
```

## 🎓 Learning Outcomes

After completing this demo, you'll understand:

### Advanced TMC Concepts
- How controllers can manage resources across multiple clusters
- The role of CRDs in TMC synchronization
- Bidirectional status propagation mechanisms
- Cross-cluster resource lifecycle management

### Practical Patterns
- Centralized controller architectures
- Regional resource submission patterns
- Global state management with local views
- Failure isolation and resilience

### Production Considerations
- Controller placement strategies
- CRD version management across clusters
- Status consistency guarantees
- Performance and scaling implications

## 🚀 Next Steps

After completing this demo:

1. **Experiment**: Modify TaskQueue specs and see how changes propagate
2. **Scale**: Add more clusters and observe behavior
3. **Extend**: Create your own CRDs and controllers
4. **Production**: Try the [Helm Deployment](../helm-deployment/) demo
5. **Deep Dive**: Read the [TMC Architecture](../../docs/content/developers/tmc/architecture.md)

## 📚 Additional Resources

- [Custom Resource Management](../../docs/content/developers/tmc/placement-controller.md)
- [Controller Development Patterns](../../docs/content/developers/tmc/syncer.md)
- [Production Deployment](../helm-deployment/)
- [TMC API Reference](../../docs/content/developers/tmc/README.md)