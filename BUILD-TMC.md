# Building KCP with TMC (Transparent Multi-Cluster) Support

This guide explains how to build KCP with the complete TMC (Transparent Multi-Cluster) solution enabled, create container images, and deploy using Helm charts.

## 🎯 Overview

The TMC-enabled KCP build includes:
- **KCP Core** - Kubernetes Control Plane functionality
- **TMC Components** - Error handling, health monitoring, metrics, recovery
- **Workload Syncer** - Bidirectional resource synchronization
- **Virtual Workspace Manager** - Cross-cluster resource aggregation
- **Placement Controller** - Intelligent workload placement

## 📋 Prerequisites

### Development Environment
```bash
# Required tools
- Go 1.21+ 
- Docker 20.10+
- kubectl 1.28+
- Helm 3.8+
- make
- git
```

### System Requirements
```bash
# Minimum resources for development
- CPU: 4 cores
- Memory: 8GB RAM  
- Disk: 20GB free space

# Recommended for production testing
- CPU: 8+ cores
- Memory: 16GB+ RAM
- Disk: 50GB+ SSD
```

## 🔧 Building KCP with TMC

### 1. Clone and Prepare Repository

```bash
# Clone the KCP repository
git clone https://github.com/kcp-dev/kcp.git
cd kcp

# Verify TMC components are present
ls -la pkg/reconciler/workload/tmc/
ls -la pkg/reconciler/workload/syncer/
```

### 2. Build TMC-Enabled Binaries

```bash
# Build all KCP binaries with TMC support
make build

# This creates the following binaries:
# - bin/kcp                    # KCP server with TMC controllers
# - bin/workload-syncer        # TMC syncer with all components
# - bin/kubectl-kcp            # KCP kubectl plugin
# - bin/kubectl-workspaces     # Workspace management plugin
```

### 3. Verify TMC Components

```bash
# Check that TMC components are included
./bin/kcp --help | grep -i tmc
./bin/workload-syncer --help | grep -i tmc

# Verify TMC imports in the binary
go tool nm ./bin/kcp | grep tmc
go tool nm ./bin/workload-syncer | grep tmc
```

### 4. Build and Test Locally

```bash
# Run KCP server with TMC enabled
./bin/kcp start \
  --root-directory=.kcp \
  --enable-tmc \
  --tmc-error-handling=true \
  --tmc-health-monitoring=true \
  --tmc-metrics=true \
  --tmc-recovery=true \
  --v=2

# In another terminal, test the syncer
export KUBECONFIG=.kcp/admin.kubeconfig
./bin/workload-syncer \
  --sync-target-name=test-target \
  --sync-target-uid=$(uuidgen) \
  --workspace-cluster=root:default \
  --kcp-kubeconfig=.kcp/admin.kubeconfig \
  --cluster-kubeconfig=~/.kube/config \
  --v=2
```

## 🐳 Container Image Creation

### 1. Multi-Stage Dockerfile

The TMC-enabled container includes all necessary components:

```dockerfile
# See docker/Dockerfile.tmc for the complete implementation
FROM golang:1.21-alpine AS builder
# ... build process with TMC components
FROM alpine:latest
# ... final image with KCP + TMC binaries
```

### 2. Build Container Images

```bash
# Build the TMC-enabled KCP image
make image-tmc

# Or build manually
docker build -f docker/Dockerfile.tmc -t kcp-tmc:latest .

# Build individual component images
docker build -f docker/Dockerfile.kcp -t kcp-server:latest .
docker build -f docker/Dockerfile.syncer -t kcp-syncer:latest .
```

### 3. Tag and Push Images

```bash
# Tag for your registry
docker tag kcp-tmc:latest your-registry.com/kcp-tmc:v0.11.0
docker tag kcp-server:latest your-registry.com/kcp-server:v0.11.0  
docker tag kcp-syncer:latest your-registry.com/kcp-syncer:v0.11.0

# Push to registry
docker push your-registry.com/kcp-tmc:v0.11.0
docker push your-registry.com/kcp-server:v0.11.0
docker push your-registry.com/kcp-syncer:v0.11.0
```

## ⎈ Helm Chart Deployment

### 1. Install KCP with TMC using Helm

```bash
# Add the KCP Helm repository (when available)
helm repo add kcp https://charts.kcp.io
helm repo update

# Or use local charts
cd charts/kcp-tmc

# Install KCP with TMC enabled
helm install kcp-tmc ./charts/kcp-tmc \
  --namespace kcp-system \
  --create-namespace \
  --set tmc.enabled=true \
  --set tmc.errorHandling.enabled=true \
  --set tmc.healthMonitoring.enabled=true \
  --set tmc.metrics.enabled=true \
  --set tmc.recovery.enabled=true \
  --set image.registry=your-registry.com \
  --set image.tag=v0.11.0
```

### 2. Deploy Syncers to Target Clusters

```bash
# Generate syncer manifests for target cluster
helm template kcp-syncer ./charts/kcp-syncer \
  --set syncTarget.name=production-east \
  --set syncTarget.workspace=root:production \
  --set kcp.endpoint=https://kcp.your-domain.com \
  --set image.registry=your-registry.com \
  --set image.tag=v0.11.0 > syncer-east.yaml

# Apply to target cluster
kubectl --context=production-east apply -f syncer-east.yaml
```

## 🏗️ Build Configuration Options

### TMC Feature Flags

```bash
# Environment variables for TMC build
export KCP_TMC_ENABLED=true
export KCP_TMC_ERROR_HANDLING=true
export KCP_TMC_HEALTH_MONITORING=true
export KCP_TMC_METRICS=true
export KCP_TMC_RECOVERY=true
export KCP_TMC_VIRTUAL_WORKSPACES=true
export KCP_TMC_PLACEMENT_CONTROLLER=true

# Build with specific TMC components
make build TMC_COMPONENTS="error-handling,health,metrics,recovery"
```

### Build Targets

```bash
# Build everything
make all

# Build only TMC components
make build-tmc

# Build for different architectures
make build GOARCH=amd64 GOOS=linux
make build GOARCH=arm64 GOOS=linux

# Cross-compile for multiple platforms
make build-cross-platform
```

## 🧪 Testing the Build

### 1. Unit Tests

```bash
# Run all tests including TMC components
make test

# Run only TMC-specific tests
make test-tmc

# Run tests with coverage
make test-coverage
```

### 2. Integration Tests

```bash
# Run integration tests with kind clusters
make test-integration

# Run TMC-specific integration tests
make test-tmc-integration
```

### 3. End-to-End Tests

```bash
# Run complete E2E tests
make test-e2e

# Run TMC E2E scenarios
make test-tmc-e2e
```

## 📊 Monitoring the Build

### Build Metrics

The TMC build process includes comprehensive metrics:

```bash
# Build time metrics
make build-metrics

# Binary size analysis
make size-analysis

# Dependency analysis
make deps-analysis
```

### Performance Validation

```bash
# Performance benchmarks
make benchmark

# Memory usage analysis
make memory-profile

# CPU profiling
make cpu-profile
```

## 🚀 Production Considerations

### 1. Security

```bash
# Build with security hardening
make build-secure

# Generate security manifests
make security-manifests

# RBAC configuration
make rbac-manifests
```

### 2. High Availability

```bash
# Build for HA deployment
make build-ha

# Generate HA Helm values
make ha-values
```

### 3. Monitoring and Observability

```bash
# Include monitoring components
make build-with-monitoring

# Generate Prometheus rules
make prometheus-rules

# Generate Grafana dashboards
make grafana-dashboards
```

## 🔍 Troubleshooting Build Issues

### Common Build Problems

**Go Module Issues**:
```bash
# Clean and rebuild modules
go mod tidy
go mod download
make clean && make build
```

**Missing TMC Components**:
```bash
# Verify TMC source files
find . -name "*.go" -path "*/tmc/*" -exec echo "Found: {}" \;

# Check build tags
go list -tags tmc ./...
```

**Container Build Failures**:
```bash
# Build with verbose output
docker build --no-cache --progress=plain -f docker/Dockerfile.tmc .

# Check Docker resources
docker system df
docker system prune
```

### Performance Issues

**Slow Builds**:
```bash
# Use build cache
export GOCACHE=/tmp/go-build-cache
export GOMODCACHE=/tmp/go-mod-cache

# Parallel builds
make -j$(nproc) build
```

**Large Images**:
```bash
# Optimize image size
make image-optimize

# Multi-stage build analysis
docker history kcp-tmc:latest
```

## 📚 Next Steps

After building KCP with TMC:

1. **Deploy with Helm**: Use the provided Helm charts for production deployment
2. **Configure Monitoring**: Set up Prometheus and Grafana for observability
3. **Scale Testing**: Test with multiple clusters and workloads
4. **Production Hardening**: Apply security policies and resource limits

For deployment instructions, see:
- [Helm Chart Documentation](./charts/kcp-tmc/README.md)
- [Production Deployment Guide](./docs/production-deployment.md)
- [TMC Tutorial Collection](./simple-tutorial/README.md)