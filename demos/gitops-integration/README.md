# TMC GitOps Integration Demo

This demo showcases TMC working seamlessly with ArgoCD for multi-cluster GitOps deployments, demonstrating how TMC enables centralized GitOps control with distributed execution across development, staging, and production environments.

## 🎯 What You'll Learn

- **Centralized GitOps management** with ArgoCD + TMC coordination
- **Multi-environment deployment pipelines** (Dev → Staging → Prod)
- **Git-driven application lifecycle management** across clusters
- **Environment-specific policies** (auto-sync vs manual approval)
- **TMC transparent multi-cluster GitOps** coordination
- **Production-ready deployment patterns** with safety gates

## 📋 Prerequisites

- **Docker** 20.10+ (running)
- **kubectl** 1.26+
- **kind** 0.17+
- **git** 2.30+
- **bash** (for running scripts)

**System Requirements**:
- 10GB+ available RAM
- 25GB+ free disk space
- Internet connection for pulling images

## 🎬 Demo Scenario

**The Challenge**: Your development team needs a robust GitOps workflow that:
- Automatically deploys to dev and staging environments
- Requires manual approval for production deployments
- Maintains consistency across multiple clusters
- Provides real-time visibility into deployment status
- Enables easy rollbacks and environment promotion

**TMC + ArgoCD Solution**: 
- ArgoCD runs on KCP cluster for centralized GitOps management
- Git repositories drive all deployments across environments
- TMC syncers provide transparent multi-cluster coordination
- Environment-specific policies ensure production safety
- Real-time monitoring shows deployment status across all clusters

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
gitops-integration/
├── README.md                    # This file
├── run-demo.sh                 # Main demo script
├── cleanup.sh                  # Cleanup script
├── validate-demo.sh            # Validation script
├── configs/                    # Cluster configurations
│   ├── kcp-host-config.yaml    # ArgoCD host cluster
│   ├── dev-cluster-config.yaml # Development environment
│   ├── staging-cluster-config.yaml # Staging environment
│   └── prod-cluster-config.yaml    # Production environment
├── manifests/                  # Kubernetes manifests
│   ├── argocd-install.yaml     # ArgoCD components
│   ├── argocd-cluster-secrets.yaml # Multi-cluster access
│   ├── dev-syncer.yaml         # Development TMC syncer
│   ├── staging-syncer.yaml     # Staging TMC syncer
│   └── prod-syncer.yaml        # Production TMC syncer
├── scripts/                    # Interactive tools
│   ├── show-app-status.sh      # Application status across environments
│   ├── monitor-gitops.sh       # Real-time GitOps dashboard
│   └── simulate-code-change.sh # Complete workflow simulation
├── git-repos/                  # Generated Git repositories
│   ├── demo-app/              # Application source repository
│   │   ├── base-app.yaml      # Base application template
│   │   └── environments/      # Environment-specific configs
│   │       ├── dev/           # Development configuration
│   │       ├── staging/       # Staging configuration
│   │       └── prod/          # Production configuration
│   └── argocd-config/         # ArgoCD application definitions
│       └── applications/      # ArgoCD Application manifests
├── kubeconfigs/               # Generated kubeconfig files
└── logs/                      # Demo execution logs
```

## 🔄 Demo Flow

### Step 1: Multi-Cluster GitOps Infrastructure
- Creates KCP cluster to host ArgoCD for centralized management
- Deploys Development, Staging, and Production clusters
- Establishes TMC syncers with GitOps integration enabled
- Configures secure connections between all clusters

### Step 2: Git Repository Setup
- Creates application source repository with multi-environment structure
- Sets up ArgoCD configuration repository with Application definitions
- Establishes environment-specific configurations and policies
- Initializes Git history with proper branching strategy

### Step 3: ArgoCD Deployment and Configuration
- Installs ArgoCD on KCP cluster for centralized GitOps control
- Configures cluster access secrets for all target environments
- Sets up ArgoCD Applications with environment-specific sync policies
- Validates ArgoCD connectivity to all managed clusters

### Step 4: Multi-Environment Application Deployment
- Deploys demo applications to Development and Staging (auto-sync)
- Configures Production environment for manual approval workflow
- Validates application deployment across all environments
- Demonstrates environment-specific configuration management

### Step 5: GitOps Workflow Demonstration
- Simulates code changes with version updates and new features
- Shows automatic synchronization to Dev and Staging environments
- Demonstrates manual approval process for Production deployment
- Validates end-to-end GitOps workflow functionality

### Step 6: Monitoring and Management Tools
- Interactive real-time dashboard for GitOps status monitoring
- Application lifecycle management across environments
- Deployment validation and troubleshooting tools
- Environment promotion and rollback capabilities

## 🎮 Interactive Features

### Real-Time GitOps Dashboard

**Use the comprehensive monitoring script for the best experience:**
```bash
./scripts/monitor-gitops.sh
```

This provides a live dashboard showing:
```bash
===============================================================
📝 TMC GitOps Integration Monitor
===============================================================
Last updated: Wed Aug  3 14:30:15 PDT 2025 | Press Ctrl+C to stop

ArgoCD Control Plane
┌─────────────────┬─────────────┬─────────────┬─────────────────┐
│ Component       │ Status      │ Replicas    │ Function        │
├─────────────────┼─────────────┼─────────────┼─────────────────┤
│ Server          │ ✅ Running  │ 1/1         │ GitOps Management│
│ Repo Server     │ ✅ Running  │ 1/1         │ GitOps Management│
│ App Controller  │ ✅ Running  │ 1/1         │ GitOps Management│
└─────────────────┴─────────────┴─────────────┴─────────────────┘

Multi-Environment Application Status
┌─────────────────┬─────────────┬─────────────┬─────────────┬─────────────────┐
│ Environment     │ App Status  │ Replicas    │ Version     │ Sync Method     │
├─────────────────┼─────────────┼─────────────┼─────────────┼─────────────────┤
│ Development     │ ✅ Running  │ 2/2         │ v1.1.0      │ 🔄 Auto        │
│ Staging         │ ✅ Running  │ 2/2         │ v1.1.0      │ 🔄 Auto        │
│ Production      │ ⚠️ Manual   │ 3/3         │ v1.0.0      │ ✋ Manual       │
└─────────────────┴─────────────┴─────────────┴─────────────┴─────────────────┘

GitOps Workflow Pipeline
Code → Git → ArgoCD → Multi-Cluster Deployment

┌─────────────────┬─────────────┬─────────────┬─────────────────┐
│ Stage           │ Version     │ Status      │ Next Action     │
├─────────────────┼─────────────┼─────────────┼─────────────────┤
│ Git Repository  │ v1.1.0      │ ✅ Latest   │ Auto-sync       │
│ Development     │ v1.1.0      │ ✅ Synced   │ Auto-promote    │
│ Staging         │ v1.1.0      │ ✅ Synced   │ Ready           │
│ Production      │ v1.0.0      │ ⚠️ Manual   │ Awaiting Approval│
└─────────────────┴─────────────┴─────────────┴─────────────────┘

🔄 Updates every 5s • Press 'h' for help • Ctrl+C to stop
```

### Interactive GitOps Commands

The monitoring dashboard supports interactive operations:
- **Press 's'** - Simulate code change and automatic deployment
- **Press 'p'** - Promote to production (manual approval)
- **Press 'r'** - Rollback development environment
- **Press 'd'** - Show detailed application logs
- **Press 'h'** - Show help menu

### Complete GitOps Workflow Simulation

Test the entire GitOps pipeline:
```bash
# Simulate version update with new features
./scripts/simulate-code-change.sh v1.2.0 "Enhanced user interface"

# This will:
# 1. Update application version in Git repository
# 2. Commit changes with proper versioning
# 3. Trigger ArgoCD automatic synchronization
# 4. Deploy to dev and staging automatically
# 5. Await manual approval for production
```

### Application Status Monitoring

```bash
# Check current status across all environments
./scripts/show-app-status.sh

# This shows:
# - ArgoCD component health
# - TMC syncer connectivity
# - Application deployment status
# - Version consistency across environments
```

## 🧪 What the Demo Shows

### 1. Multi-Environment Application Repository
```yaml
# Git repository structure for GitOps
demo-app/
├── base-app.yaml              # Base application template
└── environments/
    ├── dev/webapp.yaml        # Development-specific config
    ├── staging/webapp.yaml    # Staging-specific config
    └── prod/webapp.yaml       # Production-specific config (3 replicas)
```

### 2. ArgoCD Application Definitions
```yaml
# Development Application (Auto-sync enabled)
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: demo-webapp-dev
  namespace: argocd
spec:
  project: default
  source:
    repoURL: file:///path/to/demo-app
    targetRevision: HEAD
    path: environments/dev
  destination:
    server: https://127.0.0.1:39444  # Dev cluster
    namespace: default
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
```

```yaml
# Production Application (Manual sync required)
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: demo-webapp-prod
  namespace: argocd
spec:
  project: default
  source:
    repoURL: file:///path/to/demo-app
    targetRevision: HEAD
    path: environments/prod
  destination:
    server: https://127.0.0.1:39446  # Prod cluster
    namespace: default
  syncPolicy:
    manual: {}  # Requires explicit approval
```

### 3. TMC Syncer with GitOps Integration
```yaml
# TMC Syncer with GitOps capabilities
apiVersion: apps/v1
kind: Deployment
metadata:
  name: kcp-syncer
spec:
  template:
    spec:
      containers:
      - name: syncer
        env:
        - name: GITOPS_INTEGRATION
          value: "enabled"
        - name: ARGOCD_NAMESPACE
          value: "argocd"
        # Coordinates with ArgoCD for GitOps workflows
        # Provides TMC transparency across clusters
```

### 4. Environment-Specific Configurations
```yaml
# Development: Fast iteration, auto-sync
spec:
  replicas: 2
  syncPolicy:
    automated:
      prune: true
      selfHeal: true

# Staging: Production-like, auto-sync with validation
spec:
  replicas: 2
  syncPolicy:
    automated:
      prune: true
      selfHeal: true

# Production: High availability, manual approval
spec:
  replicas: 3
  syncPolicy:
    manual: {}
```

## 🔧 Configuration Options

### Environment Variables
```bash
# Demo behavior
DEMO_DEBUG=true                    # Enable debug output
DEMO_SKIP_CLEANUP=true             # Keep resources after demo
DEMO_PAUSE_STEPS=false             # Run without pauses

# Cluster configuration
GITOPS_KCP_PORT=39443              # ArgoCD host cluster port
GITOPS_DEV_PORT=39444              # Development cluster port
GITOPS_STAGING_PORT=39445          # Staging cluster port
GITOPS_PROD_PORT=39446             # Production cluster port

# GitOps settings
ARGOCD_SYNC_INTERVAL=30s           # Application sync frequency
AUTO_SYNC_ENVIRONMENTS="dev,staging" # Auto-sync enabled environments
MANUAL_APPROVAL_ENVIRONMENTS="prod"  # Manual approval required
```

### ArgoCD Configuration Customization
```yaml
# Custom ArgoCD Application template
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: my-application
  namespace: argocd
  labels:
    environment: production
    managed-by: tmc-gitops
spec:
  project: default
  source:
    repoURL: https://github.com/my-org/my-app
    targetRevision: main
    path: manifests/production
  destination:
    server: https://my-prod-cluster:6443
    namespace: my-app
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
    - CreateNamespace=true
    - PrunePropagationPolicy=foreground
```

### Environment-Specific Resource Scaling
```yaml
# Development: Minimal resources
spec:
  replicas: 1
  resources:
    requests:
      memory: "64Mi"
      cpu: "50m"
    limits:
      memory: "128Mi"
      cpu: "100m"

# Production: High availability resources
spec:
  replicas: 5
  resources:
    requests:
      memory: "256Mi"
      cpu: "200m"
    limits:
      memory: "512Mi"
      cpu: "500m"
```

## 📊 Monitoring and Observability

### GitOps Workflow Monitoring
```bash
# Real-time GitOps pipeline status
./scripts/monitor-gitops.sh

# Application status across environments
./scripts/show-app-status.sh

# Comprehensive validation of all components
./validate-demo.sh --check-all
```

### ArgoCD Application Management
```bash
# Check ArgoCD applications (when demo is running)
kubectl --context kind-gitops-kcp get applications -n argocd

# View application details
kubectl --context kind-gitops-kcp describe application demo-webapp-dev -n argocd

# Check ArgoCD server logs
kubectl --context kind-gitops-kcp logs deployment/argocd-server -n argocd
```

### Environment-Specific Monitoring
```bash
# Development environment
kubectl --context kind-gitops-dev get all
kubectl --context kind-gitops-dev logs deployment/demo-webapp

# Staging environment
kubectl --context kind-gitops-staging get all
kubectl --context kind-gitops-staging describe deployment/demo-webapp

# Production environment
kubectl --context kind-gitops-prod get all
kubectl --context kind-gitops-prod get events --sort-by='.lastTimestamp'
```

### TMC Syncer Health Monitoring
```bash
# Check syncer status across environments
for env in dev staging prod; do
  echo "=== $env environment ==="
  kubectl --context kind-gitops-$env get deployment kcp-syncer
  kubectl --context kind-gitops-$env logs deployment/kcp-syncer --tail=10
done
```

## 🎯 Key Learning Points

### TMC + GitOps Integration Patterns
1. **Centralized Control**: ArgoCD on KCP provides global GitOps management
2. **Distributed Execution**: Applications run on target clusters with TMC coordination
3. **Environment Policies**: Different sync policies for different environments
4. **Transparent Operations**: TMC makes multi-cluster GitOps seamless

### Production GitOps Workflows
1. **Git as Source of Truth**: All changes flow through Git repositories
2. **Environment Promotion**: Automated dev/staging, manual production
3. **Version Management**: Semantic versioning with environment tracking
4. **Rollback Capabilities**: Quick recovery from deployment issues

### Advanced GitOps Scenarios
1. **Multi-Repository Management**: Separate repos for apps and infrastructure
2. **Branch-Based Environments**: Feature branches for development environments
3. **Policy as Code**: GitOps for policy and configuration management
4. **Progressive Delivery**: Canary and blue-green deployments via GitOps

## 🔍 Troubleshooting

### Common Issues

**ArgoCD applications not syncing**:
```bash
# Check ArgoCD application status
kubectl --context kind-gitops-kcp get applications -n argocd

# View application details and events
kubectl --context kind-gitops-kcp describe application demo-webapp-dev -n argocd

# Check ArgoCD controller logs
kubectl --context kind-gitops-kcp logs deployment/argocd-application-controller -n argocd
```

**TMC syncer connectivity issues**:
```bash
# Check syncer status on each environment
kubectl --context kind-gitops-dev get deployment kcp-syncer
kubectl --context kind-gitops-staging get deployment kcp-syncer
kubectl --context kind-gitops-prod get deployment kcp-syncer

# Check syncer logs for connectivity errors
kubectl --context kind-gitops-dev logs deployment/kcp-syncer
```

**Git repository issues**:
```bash
# Verify git repositories were created properly
ls -la git-repos/
git -C git-repos/demo-app status
git -C git-repos/argocd-config status

# Check git repository content
find git-repos/ -name "*.yaml" -exec echo "=== {} ===" \; -exec cat {} \;
```

**Application deployment failures**:
```bash
# Check deployment status across environments
./scripts/show-app-status.sh

# Check specific environment
kubectl --context kind-gitops-dev describe deployment demo-webapp
kubectl --context kind-gitops-dev get events --sort-by='.lastTimestamp'
```

### Debug Mode
```bash
# Run demo with comprehensive debugging
DEMO_DEBUG=true ./run-demo.sh

# This shows:
# - All kubectl commands with contexts
# - Git repository creation and configuration
# - ArgoCD installation and setup
# - Application deployment progress
# - TMC syncer coordination
```

### Validation and Recovery
```bash
# Comprehensive validation of all components
./validate-demo.sh --check-all

# Validate specific components
./validate-demo.sh --check-argocd
./validate-demo.sh --check-syncers
./validate-demo.sh --check-gitops

# Force cleanup and restart if needed
./cleanup.sh --force
./run-demo.sh
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

# Remove everything including git repositories
./cleanup.sh --full

# Force cleanup ignoring errors
./cleanup.sh --force
```

### Git Repository Management
```bash
# Backup git repositories before cleanup
cp -r git-repos/ git-repos-backup/

# Restore git repositories after cleanup
cp -r git-repos-backup/ git-repos/
```

## 🎓 Learning Outcomes

After completing this demo, you'll understand:

### Advanced GitOps with TMC
- How TMC enables transparent multi-cluster GitOps workflows
- ArgoCD integration patterns for centralized GitOps management
- Environment-specific deployment policies and approval workflows
- Git-driven application lifecycle management across clusters

### Production GitOps Patterns
- Multi-environment pipeline design (Dev → Staging → Prod)
- Automated deployment with manual production gates
- Version management and environment promotion strategies
- Rollback and recovery procedures in GitOps workflows

### TMC GitOps Benefits
- Centralized control plane with distributed execution
- Transparent multi-cluster coordination for GitOps
- Environment isolation with global policy management
- Scalable GitOps patterns for enterprise deployments

## 🚀 Next Steps

After completing this demo:

1. **Experiment**: Modify application configurations and observe GitOps sync
2. **Extend**: Add more environments and complex deployment pipelines
3. **Integrate**: Connect with real Git repositories and CI/CD systems
4. **Scale**: Try with multiple applications and microservices
5. **Advanced**: Explore the [Policy Enforcement Demo](../policy-enforcement/) or [Multi-Tenant Demo](../multi-tenant/)

## 📚 Additional Resources

- [ArgoCD Documentation](https://argo-cd.readthedocs.io/)
- [TMC GitOps Architecture](../../docs/content/developers/tmc/gitops-integration.md)
- [Multi-Cluster GitOps Patterns](../../docs/content/developers/tmc/gitops-patterns.md)
- [Production GitOps with TMC](../../docs/content/developers/tmc/production-gitops.md)
- [GitOps Security Best Practices](../../docs/content/developers/tmc/gitops-security.md)
- [TMC API Reference](../../docs/content/developers/tmc/README.md)

## 🤝 GitOps Workflow Examples

### Development Workflow
```bash
# 1. Make code changes
./scripts/simulate-code-change.sh v1.3.0 "New dashboard features"

# 2. Monitor automatic deployment
./scripts/monitor-gitops.sh

# 3. Validate changes in dev/staging
./scripts/show-app-status.sh

# 4. Promote to production when ready
# (Interactive prompt in monitoring dashboard)
```

### Production Release Workflow
```bash
# 1. Validate staging deployment
kubectl --context kind-gitops-staging get deployment demo-webapp -o wide

# 2. Manual production approval
./scripts/monitor-gitops.sh  # Press 'p' to promote

# 3. Monitor production deployment
kubectl --context kind-gitops-prod rollout status deployment/demo-webapp

# 4. Verify production health
./validate-demo.sh --check-apps
```

### Rollback Workflow
```bash
# 1. Identify issue in production
./scripts/show-app-status.sh

# 2. Rollback to previous version
git -C git-repos/demo-app log --oneline
git -C git-repos/demo-app revert HEAD

# 3. Monitor automatic rollback
./scripts/monitor-gitops.sh
```

This comprehensive GitOps integration demo showcases how TMC and ArgoCD work together to provide enterprise-grade multi-cluster GitOps workflows with the transparency, security, and scalability needed for production environments.