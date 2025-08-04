# TMC Demos Collection

This directory contains independent, atomic demonstrations of KCP with TMC (Transparent Multi-Cluster) capabilities. Each demo is completely self-contained and can be run without any dependencies on other demos.

## 🎯 Available Demos

### 1. Hello World Demo (`hello-world/`)
**Purpose**: Basic introduction to TMC concepts and setup
**Duration**: 5-10 minutes
**Prerequisites**: Docker, kind, kubectl

**What it demonstrates**:
- Setting up KCP host cluster
- Creating east/west kind clusters  
- Installing basic TMC syncers
- Simple workload synchronization
- Basic health monitoring

**Run**: `cd hello-world && ./run-demo.sh`

### 2. Cross-Cluster Controller Demo (`cross-cluster-controller/`)
**Purpose**: Advanced cross-cluster Custom Resource management
**Duration**: 10-15 minutes
**Prerequisites**: Docker, kind, kubectl

**What it demonstrates**:
- Controller on one cluster managing CRs from multiple clusters
- Custom Resource Definition synchronization
- Bidirectional status propagation
- Real-time cross-cluster operations
- Status updates visible on all clusters

**Run**: `cd cross-cluster-controller && ./run-demo.sh`

### 3. Helm Deployment Demo (`helm-deployment/`)
**Purpose**: Production-ready deployment using Helm charts
**Duration**: 15-20 minutes
**Prerequisites**: Docker, kind, kubectl, Helm

**What it demonstrates**:
- Building TMC-enabled container images
- Deploying KCP with Helm charts
- Multi-cluster syncer deployment
- Production monitoring and observability
- GitOps-ready configuration

**Run**: `cd helm-deployment && ./run-demo.sh`

### 4. Production Setup Demo (`production-setup/`)
**Purpose**: Enterprise-grade multi-region deployment
**Duration**: 20-30 minutes
**Prerequisites**: Docker, kind, kubectl, Helm

**What it demonstrates**:
- Multi-region TMC deployment
- High availability configuration
- Advanced monitoring and alerting
- Security and RBAC setup
- Performance tuning and scaling

**Run**: `cd production-setup && ./run-demo.sh`

### 5. Disaster Recovery Demo (`disaster-recovery/`)
**Purpose**: Automated failover and recovery across regions
**Duration**: 15-20 minutes
**Prerequisites**: Docker, kind, kubectl

**What it demonstrates**:
- Multi-region cluster setup with primary/backup regions
- Automatic failover detection and triggering
- Application migration between regions
- Data synchronization and consistency
- Complete disaster recovery workflows

**Run**: `cd disaster-recovery && ./run-demo.sh`

### 6. GitOps Integration Demo (`gitops-integration/`)
**Purpose**: GitOps workflows with ArgoCD across multiple clusters
**Duration**: 20-25 minutes
**Prerequisites**: Docker, kind, kubectl

**What it demonstrates**:
- ArgoCD deployment and multi-cluster configuration
- Git-driven application lifecycle management
- Environment-specific deployment pipelines (dev → staging → prod)
- GitOps synchronization across clusters
- Real-time deployment monitoring and management

**Run**: `cd gitops-integration && ./run-demo.sh`

### 7. Multi-Tenant Demo (`multi-tenant/`)
**Purpose**: Isolated tenant workspaces across multiple clusters
**Duration**: 15-20 minutes
**Prerequisites**: Docker, kind, kubectl

**What it demonstrates**:
- Multi-tenant architecture with shared and isolated clusters
- Tenant resource isolation (network, compute, storage)
- Per-tenant quotas and security boundaries
- Cross-cluster tenant coordination
- Tenant lifecycle management and monitoring

**Run**: `cd multi-tenant && ./run-demo.sh`

### 8. Policy Enforcement Demo (`policy-enforcement/`)
**Purpose**: Global policy enforcement across multiple clusters
**Duration**: 18-22 minutes
**Prerequisites**: Docker, kind, kubectl

**What it demonstrates**:
- Centralized policy management with distributed enforcement
- Security, resource, compliance, and network policies
- Environment-specific policy tiers (relaxed, moderate, strict)
- Real-time policy violation detection and blocking
- Dynamic policy updates with automatic synchronization

**Run**: `cd policy-enforcement && ./run-demo.sh`

### 9. Progressive Rollout Demo (`progressive-rollout/`)
**Purpose**: Canary deployments and safe rollouts across clusters
**Duration**: 20-25 minutes
**Prerequisites**: Docker, kind, kubectl

**What it demonstrates**:
- Canary deployments with automated health monitoring
- Progressive promotion through environment tiers
- Blue-green production deployments with zero downtime
- Automatic rollback on detection of critical issues
- Multi-environment rollout coordination

**Run**: `cd progressive-rollout && ./run-demo.sh`

## 🚀 Quick Start

### Use the Master Launcher (Recommended)
```bash
# List all available demos
./run-all-demos.sh --list

# Run a specific demo
./run-all-demos.sh hello-world

# Run all demos sequentially
./run-all-demos.sh --all

# Run all demos without cleanup between them
./run-all-demos.sh --all --skip-cleanup
```

### Run Individual Demo
```bash
# Each demo is completely independent
cd hello-world && ./run-demo.sh
cd cross-cluster-controller && ./run-demo.sh  
cd helm-deployment && ./run-demo.sh
cd production-setup && ./run-demo.sh
```

## 📋 Demo Independence

Each demo directory contains:
- ✅ **Complete setup scripts** - No external dependencies
- ✅ **Self-contained configurations** - All YAML files included
- ✅ **Dedicated cleanup** - Removes only its own resources
- ✅ **Isolated environments** - Uses unique cluster names
- ✅ **Comprehensive documentation** - Standalone README
- ✅ **Validation scripts** - Test demo completion

## 🔧 Common Prerequisites

All demos require these basic tools:
```bash
# Required for all demos
docker --version    # 20.10+
kubectl version     # 1.26+
kind --version      # 0.17+

# Required for Helm demos only
helm version        # 3.8+

# Required for container builds
make --version      # Any recent version
```

## 📊 Demo Comparison

| Demo | Complexity | Duration | Prerequisites | Key Focus |
|------|------------|----------|---------------|-----------|
| Hello World | Basic | 5-10 min | Docker, kind | TMC basics |
| Cross-Cluster Controller | Intermediate | 10-15 min | Docker, kind | CRD sync |
| Helm Deployment | Advanced | 15-20 min | + Helm | Production |
| Production Setup | Expert | 20-30 min | + Helm | Enterprise |
| Disaster Recovery | Intermediate | 15-20 min | Docker, kind | Failover |
| GitOps Integration | Advanced | 20-25 min | Docker, kind | GitOps |
| Multi-Tenant | Intermediate | 15-20 min | Docker, kind | Tenancy |
| Policy Enforcement | Advanced | 18-22 min | Docker, kind | Governance |
| Progressive Rollout | Advanced | 20-25 min | Docker, kind | Deployments |

## 🎮 Interactive Features

Each demo includes:
- **Step-by-step guidance** with colored output
- **Wait points** for user interaction and learning
- **Real-time status displays** showing live cluster state
- **Failure simulation** and recovery demonstrations
- **Cleanup confirmation** with optional resource preservation

## 🔍 Troubleshooting

### Common Issues

**Docker not running**:
```bash
sudo systemctl start docker
# or on macOS
open -a Docker
```

**Port conflicts**:
```bash
# Each demo uses unique ports
# No conflicts between demos
```

**Cleanup between demos**:
```bash
# Each demo cleans up automatically
# Or run manual cleanup:
cd <demo-directory>
./cleanup.sh
```

### Getting Help

1. **Check demo logs**: Each demo creates logs in `./logs/`
2. **Run validation**: `./validate-demo.sh` in each demo directory
3. **Review documentation**: Each demo has a detailed README
4. **Use debug mode**: `DEMO_DEBUG=true ./run-demo.sh`

## 🏗️ Demo Architecture

### Shared Design Principles
All demos follow these patterns:
- **Atomic operation** - Complete independence
- **Idempotent execution** - Safe to run multiple times
- **Clean resource naming** - No conflicts between demos
- **Comprehensive logging** - Full audit trail
- **Graceful failure handling** - Clear error messages

### Resource Isolation
Each demo uses unique:
- **Cluster names**: `demo-<type>-<component>`
- **Namespace names**: `<demo-name>-system`
- **Port ranges**: Non-overlapping port assignments
- **Container names**: Prefixed with demo name
- **Storage paths**: Isolated data directories

## 📚 Learning Path

**Recommended order for learning**:
1. **Hello World** - Understand TMC fundamentals
2. **Cross-Cluster Controller** - See advanced CRD capabilities  
3. **Disaster Recovery** - Learn failover and resilience
4. **Multi-Tenant** - Understand isolation and resource management
5. **Policy Enforcement** - Master governance and compliance
6. **GitOps Integration** - Learn modern deployment workflows
7. **Progressive Rollout** - Master advanced deployment strategies
8. **Helm Deployment** - Learn production deployment
9. **Production Setup** - Master enterprise patterns

**For specific use cases**:
- **Developers**: Hello World → Cross-Cluster Controller → GitOps Integration
- **DevOps Engineers**: Disaster Recovery → Progressive Rollout → Policy Enforcement
- **Platform Engineers**: All demos in sequence
- **Security Engineers**: Multi-Tenant → Policy Enforcement
- **Decision Makers**: Hello World → Production Setup → Policy Enforcement

**By complexity level**:
- **Beginner**: Hello World
- **Intermediate**: Cross-Cluster Controller, Disaster Recovery, Multi-Tenant
- **Advanced**: GitOps Integration, Policy Enforcement, Progressive Rollout, Helm Deployment
- **Expert**: Production Setup

## 🔄 Demo Lifecycle

Each demo follows this lifecycle:
1. **Prerequisites Check** - Validate required tools
2. **Environment Setup** - Create isolated resources
3. **Component Installation** - Deploy TMC components
4. **Feature Demonstration** - Show key capabilities
5. **Validation** - Verify correct operation
6. **Cleanup** - Remove all created resources

## 📈 Next Steps

After running the demos:
1. **Read the documentation**: [TMC Documentation](../docs/content/developers/tmc/)
2. **Build your own images**: [BUILD-TMC.md](../BUILD-TMC.md)
3. **Deploy to real clusters**: Use the Helm charts in [charts/](../charts/)
4. **Contribute**: See [CONTRIBUTING.md](../CONTRIBUTING.md)

## 🆘 Support

- **Documentation**: Each demo directory has detailed README
- **Issues**: Report problems with specific demo names
- **Discussions**: Use GitHub discussions for questions
- **Community**: Join the KCP Slack channel