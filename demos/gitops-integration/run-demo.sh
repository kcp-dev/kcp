#!/bin/bash

# TMC GitOps Integration Demo
# Demonstrates TMC working with ArgoCD for multi-cluster GitOps deployments

set -e

# Demo configuration
DEMO_DIR="$(dirname "$0")"
CONFIGS_DIR="$DEMO_DIR/configs"
MANIFESTS_DIR="$DEMO_DIR/manifests"
GIT_REPOS_DIR="$DEMO_DIR/git-repos"
SCRIPTS_DIR="$DEMO_DIR/scripts"
KUBECONFIG_DIR="$DEMO_DIR/kubeconfigs"
LOGS_DIR="$DEMO_DIR/logs"

# Cluster configuration
GITOPS_KCP_PORT=39443
GITOPS_DEV_PORT=39444
GITOPS_STAGING_PORT=39445
GITOPS_PROD_PORT=39446

# Cluster contexts
KCP_CONTEXT="kind-gitops-kcp"
DEV_CONTEXT="kind-gitops-dev"
STAGING_CONTEXT="kind-gitops-staging"
PROD_CONTEXT="kind-gitops-prod"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
PURPLE='\033[0;35m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m' # No Color

# Unicode symbols
CHECK="✅"
CROSS="❌"
ROCKET="🚀"
CLUSTER="🔗"
GIT="📝"
WARNING="⚠️"

# Environment variables
DEMO_DEBUG="${DEMO_DEBUG:-false}"
DEMO_SKIP_CLEANUP="${DEMO_SKIP_CLEANUP:-false}"
DEMO_PAUSE_STEPS="${DEMO_PAUSE_STEPS:-true}"

# Create directories
mkdir -p "$KUBECONFIG_DIR" "$LOGS_DIR"

print_header() {
    echo -e "${BOLD}${PURPLE}================================================${NC}"
    echo -e "${BOLD}${PURPLE}${ROCKET} TMC GitOps Integration Demo${NC}"
    echo -e "${BOLD}${PURPLE}================================================${NC}"
    echo -e "${CYAN}Demonstrating TMC with ArgoCD for multi-cluster GitOps${NC}"
    echo ""
}

print_step() {
    echo -e "${BOLD}${BLUE}$1${NC}"
    if [[ "$DEMO_PAUSE_STEPS" == "true" ]]; then
        echo -e "${YELLOW}Press Enter to continue...${NC}"
        read -r
    fi
}

debug_log() {
    if [[ "$DEMO_DEBUG" == "true" ]]; then
        echo -e "${CYAN}[DEBUG] $1${NC}"
    fi
}

# Cleanup function
cleanup() {
    if [[ "$DEMO_SKIP_CLEANUP" == "true" ]]; then
        echo -e "${YELLOW}${WARNING} Skipping cleanup (DEMO_SKIP_CLEANUP=true)${NC}"
        echo -e "${CYAN}To manually cleanup later, run: ./cleanup.sh${NC}"
        return 0
    fi
    
    echo -e "${YELLOW}Cleaning up demo resources...${NC}"
    
    # Delete kind clusters
    kind delete cluster --name gitops-kcp &>/dev/null || true
    kind delete cluster --name gitops-dev &>/dev/null || true
    kind delete cluster --name gitops-staging &>/dev/null || true
    kind delete cluster --name gitops-prod &>/dev/null || true
    
    # Clean up kubeconfig files
    rm -rf "$KUBECONFIG_DIR"
    
    echo -e "${GREEN}${CHECK} Cleanup completed${NC}"
}

# Setup trap for cleanup on exit
trap cleanup EXIT

create_clusters() {
    print_step "Step 1: Creating GitOps environment (KCP + Dev + Staging + Prod)"
    
    echo -e "${BLUE}Creating KCP host cluster for GitOps coordination...${NC}"
    debug_log "kind create cluster --name gitops-kcp --config $CONFIGS_DIR/kcp-host-config.yaml"
    if kind create cluster --name gitops-kcp --config "$CONFIGS_DIR/kcp-host-config.yaml"; then
        echo -e "${GREEN}${CHECK} KCP cluster created${NC}"
    else
        echo -e "${RED}${CROSS} Failed to create KCP cluster${NC}"
        exit 1
    fi
    
    echo -e "${BLUE}Creating Development cluster...${NC}"
    debug_log "kind create cluster --name gitops-dev --config $CONFIGS_DIR/dev-cluster-config.yaml"
    if kind create cluster --name gitops-dev --config "$CONFIGS_DIR/dev-cluster-config.yaml"; then
        echo -e "${GREEN}${CHECK} Dev cluster created${NC}"
    else
        echo -e "${RED}${CROSS} Failed to create Dev cluster${NC}"
        exit 1
    fi
    
    echo -e "${BLUE}Creating Staging cluster...${NC}"
    debug_log "kind create cluster --name gitops-staging --config $CONFIGS_DIR/staging-cluster-config.yaml"
    if kind create cluster --name gitops-staging --config "$CONFIGS_DIR/staging-cluster-config.yaml"; then
        echo -e "${GREEN}${CHECK} Staging cluster created${NC}"
    else
        echo -e "${RED}${CROSS} Failed to create Staging cluster${NC}"
        exit 1
    fi
    
    echo -e "${BLUE}Creating Production cluster...${NC}"
    debug_log "kind create cluster --name gitops-prod --config $CONFIGS_DIR/prod-cluster-config.yaml"
    if kind create cluster --name gitops-prod --config "$CONFIGS_DIR/prod-cluster-config.yaml"; then
        echo -e "${GREEN}${CHECK} Prod cluster created${NC}"
    else
        echo -e "${RED}${CROSS} Failed to create Prod cluster${NC}"
        exit 1
    fi
    
    echo -e "${BLUE}Waiting for clusters to be ready (this may take 3-4 minutes)...${NC}"
    
    # Wait for clusters to be ready
    for context in "$KCP_CONTEXT" "$DEV_CONTEXT" "$STAGING_CONTEXT" "$PROD_CONTEXT"; do
        echo -e "${CYAN}Waiting for $context to be ready...${NC}"
        timeout=300
        while ! kubectl --context "$context" get nodes &>/dev/null && [[ $timeout -gt 0 ]]; do
            sleep 5
            timeout=$((timeout - 5))
        done
        
        if [[ $timeout -le 0 ]]; then
            echo -e "${RED}${CROSS} Timeout waiting for $context${NC}"
            exit 1
        fi
        
        echo -e "${GREEN}${CHECK} $context is ready${NC}"
    done
}

setup_tmc_syncers() {
    print_step "Step 2: Setting up TMC syncers for multi-cluster GitOps coordination"
    
    # Extract kubeconfigs with correct server addresses
    echo -e "${BLUE}Extracting and configuring kubeconfigs...${NC}"
    
    kind get kubeconfig --name gitops-kcp > "$KUBECONFIG_DIR/kcp-admin.kubeconfig"
    kind get kubeconfig --name gitops-dev > "$KUBECONFIG_DIR/dev-admin.kubeconfig"
    kind get kubeconfig --name gitops-staging > "$KUBECONFIG_DIR/staging-admin.kubeconfig"
    kind get kubeconfig --name gitops-prod > "$KUBECONFIG_DIR/prod-admin.kubeconfig"
    
    # Fix server addresses (kind uses 0.0.0.0 but certs are for 127.0.0.1)
    sed -i "s|server: https://0.0.0.0:$GITOPS_KCP_PORT|server: https://127.0.0.1:$GITOPS_KCP_PORT|" "$KUBECONFIG_DIR/kcp-admin.kubeconfig"
    sed -i "s|server: https://0.0.0.0:$GITOPS_DEV_PORT|server: https://127.0.0.1:$GITOPS_DEV_PORT|" "$KUBECONFIG_DIR/dev-admin.kubeconfig"
    sed -i "s|server: https://0.0.0.0:$GITOPS_STAGING_PORT|server: https://127.0.0.1:$GITOPS_STAGING_PORT|" "$KUBECONFIG_DIR/staging-admin.kubeconfig"
    sed -i "s|server: https://0.0.0.0:$GITOPS_PROD_PORT|server: https://127.0.0.1:$GITOPS_PROD_PORT|" "$KUBECONFIG_DIR/prod-admin.kubeconfig"
    
    echo -e "${GREEN}${CHECK} Kubeconfigs configured${NC}"
    
    # Deploy TMC syncers (simulated for demo)
    echo -e "${BLUE}Deploying TMC syncers for GitOps coordination...${NC}"
    
    # Create syncer deployments on each cluster
    kubectl --context "$DEV_CONTEXT" apply -f "$MANIFESTS_DIR/dev-syncer.yaml"
    kubectl --context "$STAGING_CONTEXT" apply -f "$MANIFESTS_DIR/staging-syncer.yaml"
    kubectl --context "$PROD_CONTEXT" apply -f "$MANIFESTS_DIR/prod-syncer.yaml"
    
    echo -e "${GREEN}${CHECK} TMC syncers deployed${NC}"
}

setup_git_repositories() {
    print_step "Step 3: Setting up Git repositories for GitOps workflows"
    
    echo -e "${BLUE}Creating local Git repositories for demo...${NC}"
    
    # Create application repository
    local app_repo="$GIT_REPOS_DIR/demo-app"
    mkdir -p "$app_repo"
    cd "$app_repo"
    
    git init --initial-branch=main
    git config user.name "TMC Demo"
    git config user.email "demo@tmc.example"
    
    # Create application manifests
    mkdir -p environments/{dev,staging,prod}
    
    # Create base application
    cat > base-app.yaml << 'EOF'
apiVersion: apps/v1
kind: Deployment
metadata:
  name: demo-webapp
  labels:
    app: demo-webapp
    managed-by: argocd
spec:
  replicas: 2
  selector:
    matchLabels:
      app: demo-webapp
  template:
    metadata:
      labels:
        app: demo-webapp
    spec:
      containers:
      - name: webapp
        image: nginx:alpine
        ports:
        - containerPort: 80
        env:
        - name: ENVIRONMENT
          value: "REPLACE_ENVIRONMENT"
        - name: VERSION
          value: "v1.0.0"
        volumeMounts:
        - name: webapp-content
          mountPath: /usr/share/nginx/html
        resources:
          requests:
            memory: "64Mi"
            cpu: "50m"
          limits:
            memory: "128Mi"
            cpu: "100m"
      volumes:
      - name: webapp-content
        configMap:
          name: webapp-content
---
apiVersion: v1
kind: Service
metadata:
  name: demo-webapp-svc
  labels:
    app: demo-webapp
spec:
  selector:
    app: demo-webapp
  ports:
  - name: http
    port: 80
    targetPort: 80
  type: ClusterIP
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: webapp-content
  labels:
    app: demo-webapp
data:
  index.html: |
    <!DOCTYPE html>
    <html>
    <head>
        <title>TMC GitOps Demo - REPLACE_ENVIRONMENT</title>
        <style>
            body { font-family: Arial, sans-serif; background: REPLACE_BACKGROUND; margin: 0; padding: 20px; }
            .container { max-width: 800px; margin: 0 auto; background: white; padding: 30px; border-radius: 10px; box-shadow: 0 4px 6px rgba(0,0,0,0.1); }
            .header { background: REPLACE_HEADER_COLOR; color: white; padding: 20px; border-radius: 5px; margin-bottom: 20px; }
            .status { background: #4caf50; color: white; padding: 15px; border-radius: 5px; margin: 10px 0; }
            .environment { background: #e3f2fd; padding: 15px; border-radius: 5px; border-left: 4px solid #2196f3; }
        </style>
    </head>
    <body>
        <div class="container">
            <div class="header">
                <h1>🚀 TMC GitOps Integration Demo</h1>
                <h2>Environment: REPLACE_ENVIRONMENT</h2>
            </div>
            
            <div class="status">
                <h3>✅ Application Status: Running</h3>
                <p>This application is deployed via ArgoCD + TMC GitOps workflow.</p>
            </div>
            
            <div class="environment">
                <h3>📍 Environment Details</h3>
                <p><strong>Environment:</strong> REPLACE_ENVIRONMENT</p>
                <p><strong>Cluster:</strong> REPLACE_CLUSTER</p>
                <p><strong>Deployment Method:</strong> ArgoCD + TMC</p>
                <p><strong>Git Sync:</strong> Automatic</p>
                <p><strong>Version:</strong> v1.0.0</p>
            </div>
            
            <div style="margin-top: 20px;">
                <h3>🔄 GitOps Features</h3>
                <ul>
                    <li>Git-driven deployments</li>
                    <li>Multi-cluster synchronization</li>
                    <li>Automatic rollbacks</li>
                    <li>Environment promotion</li>
                </ul>
            </div>
        </div>
    </body>
    </html>
EOF
    
    # Create environment-specific configurations
    # Development environment
    sed 's/REPLACE_ENVIRONMENT/Development/g; s/REPLACE_BACKGROUND/#e8f5e8/g; s/REPLACE_HEADER_COLOR/#4caf50/g; s/REPLACE_CLUSTER/gitops-dev/g' base-app.yaml > environments/dev/webapp.yaml
    
    # Staging environment
    sed 's/REPLACE_ENVIRONMENT/Staging/g; s/REPLACE_BACKGROUND/#fff3e0/g; s/REPLACE_HEADER_COLOR/#ff9800/g; s/REPLACE_CLUSTER/gitops-staging/g' base-app.yaml > environments/staging/webapp.yaml
    
    # Production environment - higher replica count
    sed 's/REPLACE_ENVIRONMENT/Production/g; s/REPLACE_BACKGROUND/#ffebee/g; s/REPLACE_HEADER_COLOR/#f44336/g; s/REPLACE_CLUSTER/gitops-prod/g; s/replicas: 2/replicas: 3/g' base-app.yaml > environments/prod/webapp.yaml
    
    # Commit to repository
    git add .
    git commit -m "Initial commit: Multi-environment webapp for TMC GitOps demo"
    
    echo -e "${GREEN}${CHECK} Application repository created at $app_repo${NC}"
    
    # Create ArgoCD configuration repository
    local argocd_repo="$GIT_REPOS_DIR/argocd-config"
    mkdir -p "$argocd_repo"
    cd "$argocd_repo"
    
    git init --initial-branch=main
    git config user.name "TMC Demo"
    git config user.email "demo@tmc.example"
    
    # Create ArgoCD applications for each environment
    mkdir -p applications
    
    # Create ArgoCD Application manifests
    cat > applications/dev-app.yaml << EOF
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: demo-webapp-dev
  namespace: argocd
  labels:
    environment: development
    managed-by: tmc-gitops
spec:
  project: default
  source:
    repoURL: file://$app_repo
    targetRevision: HEAD
    path: environments/dev
  destination:
    server: https://127.0.0.1:$GITOPS_DEV_PORT
    namespace: default
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
    - CreateNamespace=true
EOF

    cat > applications/staging-app.yaml << EOF
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: demo-webapp-staging
  namespace: argocd
  labels:
    environment: staging
    managed-by: tmc-gitops
spec:
  project: default
  source:
    repoURL: file://$app_repo
    targetRevision: HEAD
    path: environments/staging
  destination:
    server: https://127.0.0.1:$GITOPS_STAGING_PORT
    namespace: default
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
    - CreateNamespace=true
EOF

    cat > applications/prod-app.yaml << EOF
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: demo-webapp-prod
  namespace: argocd
  labels:
    environment: production
    managed-by: tmc-gitops
spec:
  project: default
  source:
    repoURL: file://$app_repo
    targetRevision: HEAD
    path: environments/prod
  destination:
    server: https://127.0.0.1:$GITOPS_PROD_PORT
    namespace: default
  syncPolicy:
    manual: {}
    syncOptions:
    - CreateNamespace=true
EOF
    
    git add .
    git commit -m "Initial ArgoCD applications for multi-cluster GitOps"
    
    echo -e "${GREEN}${CHECK} ArgoCD configuration repository created at $argocd_repo${NC}"
    
    # Return to demo directory
    cd "$DEMO_DIR"
}

deploy_argocd() {
    print_step "Step 4: Deploying ArgoCD to KCP cluster for centralized GitOps management"
    
    echo -e "${BLUE}Installing ArgoCD on KCP cluster...${NC}"
    
    # Create ArgoCD namespace
    kubectl --context "$KCP_CONTEXT" create namespace argocd --dry-run=client -o yaml | kubectl --context "$KCP_CONTEXT" apply -f -
    
    # Deploy ArgoCD (simplified version for demo)
    kubectl --context "$KCP_CONTEXT" apply -f "$MANIFESTS_DIR/argocd-install.yaml"
    
    echo -e "${BLUE}Waiting for ArgoCD to be ready...${NC}"
    kubectl --context "$KCP_CONTEXT" wait --for=condition=available --timeout=300s deployment/argocd-server -n argocd || true
    
    # Configure ArgoCD with cluster access
    echo -e "${BLUE}Configuring ArgoCD cluster access...${NC}"
    kubectl --context "$KCP_CONTEXT" apply -f "$MANIFESTS_DIR/argocd-cluster-secrets.yaml"
    
    echo -e "${GREEN}${CHECK} ArgoCD deployed and configured${NC}"
}

deploy_applications() {
    print_step "Step 5: Deploying multi-environment applications via ArgoCD + TMC"
    
    echo -e "${BLUE}Creating ArgoCD Applications for all environments...${NC}"
    
    # Deploy ArgoCD Applications
    kubectl --context "$KCP_CONTEXT" apply -f "$GIT_REPOS_DIR/argocd-config/applications/"
    
    echo -e "${BLUE}Waiting for applications to sync and deploy...${NC}"
    
    # Wait for applications to be created
    sleep 10
    
    # Check ArgoCD application status
    echo -e "${CYAN}ArgoCD Application Status:${NC}"
    kubectl --context "$KCP_CONTEXT" get applications -n argocd || true
    
    echo -e "${BLUE}Waiting for application deployments across clusters...${NC}"
    
    # Wait for deployments to be ready in each environment
    local environments=("$DEV_CONTEXT:Development" "$STAGING_CONTEXT:Staging")  # Prod is manual sync
    
    for env_info in "${environments[@]}"; do
        IFS=':' read -r context env_name <<< "$env_info"
        echo -e "${CYAN}Waiting for $env_name deployment...${NC}"
        
        # Wait for deployment to exist and be ready
        timeout=180
        while ! kubectl --context "$context" get deployment demo-webapp &>/dev/null && [[ $timeout -gt 0 ]]; do
            sleep 5
            timeout=$((timeout - 5))
        done
        
        if kubectl --context "$context" get deployment demo-webapp &>/dev/null; then
            kubectl --context "$context" wait --for=condition=available --timeout=300s deployment/demo-webapp || true
            echo -e "${GREEN}${CHECK} $env_name application deployed${NC}"
        else
            echo -e "${YELLOW}${WARNING} $env_name deployment not found, may need manual sync${NC}"
        fi
    done
    
    echo -e "${GREEN}${CHECK} Multi-environment applications deployed${NC}"
}

demonstrate_gitops_workflow() {
    print_step "Step 6: Demonstrating GitOps workflow - code change to production"
    
    echo -e "${CYAN}Starting GitOps workflow demonstration...${NC}"
    echo -e "${YELLOW}This will show the complete flow: Code Change → Git → ArgoCD → Multi-Cluster Deployment${NC}"
    echo ""
    
    # Show initial state
    echo -e "${BLUE}Initial application state across environments:${NC}"
    "$SCRIPTS_DIR/show-app-status.sh"
    
    if [[ "$DEMO_PAUSE_STEPS" == "true" ]]; then
        echo -e "${YELLOW}Press Enter to simulate a code change (version update)...${NC}"
        read -r
    fi
    
    # Simulate code change
    echo -e "${BLUE}${GIT} Simulating code change: Updating application to v1.1.0...${NC}"
    
    local app_repo="$GIT_REPOS_DIR/demo-app"
    cd "$app_repo"
    
    # Update version in all environments
    sed -i 's/v1.0.0/v1.1.0/g' environments/*/webapp.yaml
    
    # Update a feature in dev first
    sed -i 's/GitOps Features/New GitOps Features (v1.1.0)/g' environments/dev/webapp.yaml
    
    git add .
    git commit -m "feat: update to v1.1.0 with new features

- Enhanced GitOps workflow demonstration
- Improved multi-cluster synchronization
- Added version tracking"
    
    echo -e "${GREEN}${CHECK} Code changes committed to Git${NC}"
    
    cd "$DEMO_DIR"
    
    echo -e "${BLUE}ArgoCD will now automatically sync the changes...${NC}"
    sleep 5
    
    # Force ArgoCD sync for demo purposes
    echo -e "${CYAN}Triggering ArgoCD synchronization...${NC}"
    
    # Note: In a real demo, we'd use argocd CLI to sync applications
    # For this demo, we'll simulate the sync by directly applying changes
    echo -e "${YELLOW}(Simulating ArgoCD auto-sync - in real deployment, ArgoCD handles this automatically)${NC}"
    
    # Apply updated manifests to dev and staging (auto-sync enabled)
    kubectl --context "$DEV_CONTEXT" apply -f "$app_repo/environments/dev/webapp.yaml" || true
    kubectl --context "$STAGING_CONTEXT" apply -f "$app_repo/environments/staging/webapp.yaml" || true
    
    sleep 5
    
    echo -e "${BLUE}Updated application status across environments:${NC}"
    "$SCRIPTS_DIR/show-app-status.sh"
    
    if [[ "$DEMO_PAUSE_STEPS" == "true" ]]; then
        echo -e "${YELLOW}Press Enter to promote to production (manual approval required)...${NC}"
        read -r
    fi
    
    # Production deployment (manual sync)
    echo -e "${BLUE}Production deployment requires manual approval...${NC}"
    echo -e "${CYAN}Deploying to production cluster...${NC}"
    
    kubectl --context "$PROD_CONTEXT" apply -f "$app_repo/environments/prod/webapp.yaml" || true
    kubectl --context "$PROD_CONTEXT" wait --for=condition=available --timeout=300s deployment/demo-webapp || true
    
    echo -e "${GREEN}${CHECK} Production deployment complete${NC}"
    
    echo -e "${BLUE}Final application status across all environments:${NC}"
    "$SCRIPTS_DIR/show-app-status.sh"
    
    echo -e "${GREEN}${CHECK} GitOps workflow demonstration complete${NC}"
}

show_monitoring_demo() {
    print_step "Step 7: GitOps monitoring and management tools"
    
    echo -e "${CYAN}The demo includes comprehensive GitOps monitoring tools:${NC}"
    echo ""
    echo -e "${YELLOW}Real-time GitOps dashboard:${NC}"
    echo -e "${BOLD}  ./scripts/monitor-gitops.sh${NC}"
    echo ""
    echo -e "${CYAN}This provides a live view of:${NC}"
    echo -e "  • ArgoCD application sync status"
    echo -e "  • Multi-cluster deployment health"
    echo -e "  • Git repository synchronization"
    echo -e "  • Environment promotion status"
    echo ""
    echo -e "${YELLOW}Application management tools:${NC}"
    echo -e "${BOLD}  ./scripts/promote-to-staging.sh    # Promote dev changes to staging${NC}"
    echo -e "${BOLD}  ./scripts/promote-to-production.sh # Deploy to production${NC}"
    echo -e "${BOLD}  ./scripts/rollback-environment.sh  # Rollback any environment${NC}"
    echo ""
    echo -e "${YELLOW}GitOps workflow simulation:${NC}"
    echo -e "${BOLD}  ./scripts/simulate-code-change.sh  # Test complete GitOps flow${NC}"
    echo ""
}

# Main execution
main() {
    print_header
    
    echo -e "${CYAN}This demo will show:${NC}"
    echo -e "  • Multi-cluster GitOps setup with ArgoCD + TMC"
    echo -e "  • Git-driven deployments across Dev/Staging/Prod"
    echo -e "  • Automatic synchronization and manual approvals"
    echo -e "  • Environment promotion workflows"
    echo -e "  • Multi-cluster application lifecycle management"
    echo ""
    
    create_clusters
    setup_tmc_syncers
    setup_git_repositories
    deploy_argocd
    deploy_applications
    demonstrate_gitops_workflow
    show_monitoring_demo
    
    echo -e "${BOLD}${GREEN}${ROCKET} GitOps Integration Demo Complete!${NC}"
    echo ""
    echo -e "${CYAN}Key TMC + GitOps benefits demonstrated:${NC}"
    echo -e "  ${CHECK} Centralized GitOps control with multi-cluster deployment"
    echo -e "  ${CHECK} Git-driven application lifecycle management"
    echo -e "  ${CHECK} Environment-specific configurations and policies"
    echo -e "  ${CHECK} Automated dev/staging with manual production approval"
    echo -e "  ${CHECK} TMC transparent multi-cluster coordination"
    echo ""
}

# Check for help
if [[ "${1:-}" == "help" || "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
    echo "TMC GitOps Integration Demo"
    echo ""
    echo "This demo showcases TMC working with ArgoCD for"
    echo "multi-cluster GitOps deployments and workflows."
    echo ""
    echo "Usage: $0"
    echo ""
    echo "Environment variables:"
    echo "  DEMO_DEBUG=true          Enable debug output"
    echo "  DEMO_SKIP_CLEANUP=true   Keep resources after demo"
    echo "  DEMO_PAUSE_STEPS=false   Run without pauses"
    echo ""
    exit 0
fi

# Run the demo
main "$@"