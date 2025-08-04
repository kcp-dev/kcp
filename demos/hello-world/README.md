# TMC Hello World Demo

This demo provides a basic introduction to KCP with TMC (Transparent Multi-Cluster) capabilities. It's designed to be completely self-contained and independent of any other demos.

## 🎯 What You'll Learn

- Basic TMC architecture and concepts
- Setting up KCP with kind clusters
- Installing and configuring TMC syncers
- Simple workload synchronization between clusters
- Basic health monitoring and status checking

## 📋 Prerequisites

- **Docker** 20.10+ (running)
- **kubectl** 1.26+
- **kind** 0.17+
- **bash** (for running scripts)

**System Requirements**:
- 4GB+ available RAM
- 10GB+ free disk space
- Internet connection for pulling images

## 🚀 Quick Start

```bash
# Run the complete demo
./run-demo.sh

# Or run with debug output
DEMO_DEBUG=true ./run-demo.sh
```

## 📁 Demo Contents

```
hello-world/
├── README.md                 # This file
├── run-demo.sh              # Main demo script
├── cleanup.sh               # Cleanup script
├── validate-demo.sh         # Validation script
├── configs/                 # Configuration files
│   ├── kcp-host-config.yaml
│   ├── east-cluster-config.yaml
│   └── west-cluster-config.yaml
├── manifests/               # Kubernetes manifests
│   ├── hello-east.yaml
│   ├── hello-west.yaml
│   └── sync-targets.yaml
├── kubeconfigs/             # Generated kubeconfig files
└── logs/                    # Demo execution logs
```

## 🔄 Demo Flow

### Step 1: Environment Setup
- Checks prerequisites
- Creates unique kind clusters
- Sets up network connectivity

### Step 2: KCP Installation
- Deploys KCP host cluster
- Configures basic TMC components
- Validates KCP readiness

### Step 3: Syncer Installation
- Deploys syncers to east and west clusters
- Establishes secure connections to KCP
- Validates syncer connectivity

### Step 4: Workload Demonstration
- Deploys hello-world apps to different clusters
- Shows automatic synchronization
- Demonstrates status propagation

### Step 5: Validation
- Verifies all components are healthy
- Shows cross-cluster visibility
- Demonstrates TMC features

## 🎮 Interactive Features

The demo includes several interactive elements:

### Real-time Status Display
```bash
=== Cluster Status ===
✅ KCP Host: Running (3 nodes)
✅ East Cluster: Connected (1 node)
✅ West Cluster: Connected (1 node)

=== Syncer Status ===
✅ East Syncer: Healthy (sync active)
✅ West Syncer: Healthy (sync active)

=== Workload Status ===
🔄 hello-east: Deployed → Syncing → Running
🔄 hello-west: Deployed → Syncing → Running
```

### Wait Points for Learning
The demo pauses at key moments to explain concepts:
- TMC architecture overview
- Syncer connection process
- Workload synchronization flow
- Status propagation mechanics

## 🧪 What the Demo Shows

### 1. Transparent Multi-Cluster Operations
```bash
# Deploy to east cluster
kubectl apply -f hello-east.yaml

# Automatically visible on west cluster
kubectl --context kind-hello-west get deployments
# Shows: hello-east deployment (synced from east)
```

### 2. Status Synchronization
```bash
# Pod status from east cluster
kubectl --context kind-hello-east get pods
# NAME           READY   STATUS    
# hello-east-*   1/1     Running

# Same status visible on KCP
kubectl --context kind-hello-kcp get pods
# Shows aggregated view from both clusters
```

### 3. Cross-Cluster Service Discovery
```bash
# Services automatically accessible across clusters
curl http://hello-east.default.svc.cluster.local
curl http://hello-west.default.svc.cluster.local
# Both work from any cluster
```

## 🔧 Configuration Options

### Environment Variables
```bash
# Demo behavior
DEMO_DEBUG=true           # Enable debug output
DEMO_SKIP_CLEANUP=true    # Keep resources after demo
DEMO_PAUSE_STEPS=false    # Run without pauses

# Cluster configuration
HELLO_KCP_PORT=36443      # KCP API server port
HELLO_EAST_PORT=36444     # East cluster port
HELLO_WEST_PORT=36445     # West cluster port

# Resource limits
HELLO_CPU_LIMIT=1000m     # CPU limit per cluster
HELLO_MEMORY_LIMIT=2Gi    # Memory limit per cluster
```

### Custom Workloads
You can modify the demo workloads:
```yaml
# manifests/hello-east.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: hello-east
  labels:
    demo: hello-world
    cluster: east
spec:
  replicas: 2
  selector:
    matchLabels:
      app: hello-east
  template:
    metadata:
      labels:
        app: hello-east
        demo: hello-world
    spec:
      containers:
      - name: hello
        image: nginx:latest
        ports:
        - containerPort: 80
```

## 📊 Monitoring and Observability

### Health Checks
```bash
# Check overall demo health
./validate-demo.sh

# Check specific components with kubectl
kubectl --context kind-hello-kcp get pods
kubectl --context kind-hello-east get pods
kubectl --context kind-hello-west get pods
```

### Logs and Debugging
```bash
# View demo logs
cat logs/demo-$(date +%Y%m%d).log

# Check cluster logs
kind get logs --name hello-kcp
kind get logs --name hello-east
kind get logs --name hello-west

# Debug syncer issues
kubectl --context kind-hello-east logs -l app=syncer
kubectl --context kind-hello-west logs -l app=syncer
```

## 🧹 Cleanup

### Automatic Cleanup
The demo automatically cleans up unless you specify otherwise:
```bash
# Keep resources for exploration
DEMO_SKIP_CLEANUP=true ./run-demo.sh

# Manual cleanup anytime
./cleanup.sh
```

### Manual Cleanup
```bash
# Remove only demo resources
./cleanup.sh --demo-only

# Remove everything including kind clusters
./cleanup.sh --full

# Force cleanup (ignore errors)
./cleanup.sh --force
```

## 🔍 Troubleshooting

### Common Issues

**Docker not running**:
```bash
# Start Docker
sudo systemctl start docker
# or on macOS: open -a Docker
```

**Port conflicts**:
```bash
# Check what's using ports
sudo lsof -i :36443
sudo lsof -i :36444
sudo lsof -i :36445

# The demo uses unique ports to avoid conflicts
```

**Kind clusters not starting**:
```bash
# Check Docker resources
docker system df
docker system prune  # if needed

# Check available resources
free -h  # Memory
df -h    # Disk space
```

**Syncer connection issues**:
```bash
# Check network connectivity
docker network ls
docker network inspect kind

# Verify kubeconfig
kubectl --kubeconfig=./kubeconfigs/kcp-admin.kubeconfig cluster-info
```

### Debug Mode
```bash
# Run with full debug output
DEMO_DEBUG=true ./run-demo.sh

# This will show:
# - All kubectl commands executed
# - Detailed cluster status
# - Network configuration
# - Resource creation steps
```

## 🎓 Learning Outcomes

After completing this demo, you'll understand:

### TMC Concepts
- How TMC makes multi-cluster operations transparent
- The role of syncers in resource synchronization
- How status propagates between clusters
- Basic TMC architecture patterns

### Practical Skills
- Setting up KCP with kind clusters
- Configuring TMC syncers
- Deploying workloads across clusters
- Monitoring TMC operations
- Troubleshooting common issues

### Key Insights
- Multi-cluster feels like single-cluster
- Resources are automatically synchronized
- Status updates are bidirectional
- TMC handles complexity transparently

## 🚀 Next Steps

After completing this demo:

1. **Try modifications**: Edit the workload manifests and see how changes propagate
2. **Explore other demos**: Try the [Cross-Cluster Controller](../cross-cluster-controller/) demo
3. **Read documentation**: Review the [TMC documentation](../../docs/content/developers/tmc/)
4. **Build your own**: Use the [BUILD guide](../../BUILD-TMC.md) to create custom images

## 📚 Additional Resources

- [TMC Architecture Overview](../../docs/content/developers/tmc/architecture.md)
- [Workload Syncer Details](../../docs/content/developers/tmc/syncer.md)
- [Production Deployment](../helm-deployment/)
- [KCP Official Documentation](https://docs.kcp.io)