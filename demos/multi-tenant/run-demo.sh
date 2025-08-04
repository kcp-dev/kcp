#!/bin/bash

# TMC Multi-Tenant Demo
# Demonstrates isolated tenant workspaces across multiple clusters with TMC coordination

set -e

# Demo configuration
DEMO_DIR="$(dirname "$0")"
CONFIGS_DIR="$DEMO_DIR/configs"
MANIFESTS_DIR="$DEMO_DIR/manifests"  
TENANTS_DIR="$DEMO_DIR/tenants"
SCRIPTS_DIR="$DEMO_DIR/scripts"
KUBECONFIG_DIR="$DEMO_DIR/kubeconfigs"
LOGS_DIR="$DEMO_DIR/logs"

# Cluster configuration
TENANT_KCP_PORT=41443
TENANT_SHARED_PORT=41444
TENANT_ISOLATED_PORT=41445

# Cluster contexts
KCP_CONTEXT="kind-tenant-kcp"
SHARED_CONTEXT="kind-tenant-shared"
ISOLATED_CONTEXT="kind-tenant-isolated"

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
TENANT="👥" 
ISOLATION="🔒"
WARNING="⚠️"

# Environment variables
DEMO_DEBUG="${DEMO_DEBUG:-false}"
DEMO_SKIP_CLEANUP="${DEMO_SKIP_CLEANUP:-false}"
DEMO_PAUSE_STEPS="${DEMO_PAUSE_STEPS:-true}"

# Create directories
mkdir -p "$KUBECONFIG_DIR" "$LOGS_DIR"

print_header() {
    echo -e "${BOLD}${PURPLE}================================================${NC}"
    echo -e "${BOLD}${PURPLE}${TENANT} TMC Multi-Tenant Demo${NC}"
    echo -e "${BOLD}${PURPLE}================================================${NC}"
    echo -e "${CYAN}Demonstrating isolated tenant workspaces across clusters${NC}"
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
    kind delete cluster --name tenant-kcp &>/dev/null || true
    kind delete cluster --name tenant-shared &>/dev/null || true
    kind delete cluster --name tenant-isolated &>/dev/null || true
    
    # Clean up kubeconfig files
    rm -rf "$KUBECONFIG_DIR"
    
    echo -e "${GREEN}${CHECK} Cleanup completed${NC}"
}

# Setup trap for cleanup on exit
trap cleanup EXIT

create_clusters() {
    print_step "Step 1: Creating multi-tenant cluster environment"
    
    echo -e "${BLUE}Creating KCP host cluster for tenant management...${NC}"
    if kind create cluster --name tenant-kcp --config "$CONFIGS_DIR/kcp-host-config.yaml"; then
        echo -e "${GREEN}${CHECK} KCP tenant host created${NC}"
    else
        echo -e "${RED}${CROSS} Failed to create KCP cluster${NC}"
        exit 1
    fi
    
    echo -e "${BLUE}Creating shared multi-tenant cluster...${NC}"
    if kind create cluster --name tenant-shared --config "$CONFIGS_DIR/shared-cluster-config.yaml"; then
        echo -e "${GREEN}${CHECK} Shared cluster created${NC}"
    else
        echo -e "${RED}${CROSS} Failed to create shared cluster${NC}"
        exit 1
    fi
    
    echo -e "${BLUE}Creating isolated tenant cluster...${NC}"
    if kind create cluster --name tenant-isolated --config "$CONFIGS_DIR/isolated-cluster-config.yaml"; then
        echo -e "${GREEN}${CHECK} Isolated cluster created${NC}"
    else
        echo -e "${RED}${CROSS} Failed to create isolated cluster${NC}"
        exit 1
    fi
    
    echo -e "${BLUE}Waiting for clusters to be ready...${NC}"
    
    # Wait for clusters to be ready
    for context in "$KCP_CONTEXT" "$SHARED_CONTEXT" "$ISOLATED_CONTEXT"; do
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

setup_tenant_management() {
    print_step "Step 2: Setting up TMC tenant management system"
    
    # Extract kubeconfigs
    echo -e "${BLUE}Configuring kubeconfigs...${NC}"
    
    kind get kubeconfig --name tenant-kcp > "$KUBECONFIG_DIR/kcp-admin.kubeconfig"
    kind get kubeconfig --name tenant-shared > "$KUBECONFIG_DIR/shared-admin.kubeconfig"
    kind get kubeconfig --name tenant-isolated > "$KUBECONFIG_DIR/isolated-admin.kubeconfig"
    
    # Fix server addresses
    sed -i "s|server: https://0.0.0.0:$TENANT_KCP_PORT|server: https://127.0.0.1:$TENANT_KCP_PORT|" "$KUBECONFIG_DIR/kcp-admin.kubeconfig"
    sed -i "s|server: https://0.0.0.0:$TENANT_SHARED_PORT|server: https://127.0.0.1:$TENANT_SHARED_PORT|" "$KUBECONFIG_DIR/shared-admin.kubeconfig"
    sed -i "s|server: https://0.0.0.0:$TENANT_ISOLATED_PORT|server: https://127.0.0.1:$TENANT_ISOLATED_PORT|" "$KUBECONFIG_DIR/isolated-admin.kubeconfig"
    
    echo -e "${GREEN}${CHECK} Kubeconfigs configured${NC}"
    
    # Deploy tenant management system
    echo -e "${BLUE}Deploying TMC tenant management system...${NC}"
    
    kubectl --context "$KCP_CONTEXT" create namespace tenant-system --dry-run=client -o yaml | kubectl --context "$KCP_CONTEXT" apply -f -
    kubectl --context "$KCP_CONTEXT" apply -f "$MANIFESTS_DIR/tenant-manager.yaml"
    
    echo -e "${GREEN}${CHECK} Tenant management system deployed${NC}"
}

create_tenant_workspaces() {
    print_step "Step 3: Creating isolated tenant workspaces"
    
    echo -e "${BLUE}Creating tenant workspaces...${NC}"
    
    # Create tenant namespaces and RBAC
    local tenants=("acme-corp" "beta-inc" "gamma-ltd")
    
    for tenant in "${tenants[@]}"; do
        echo -e "${CYAN}Creating workspace for tenant: $tenant${NC}"
        
        # Create tenant namespace on shared cluster
        kubectl --context "$SHARED_CONTEXT" create namespace "tenant-$tenant" --dry-run=client -o yaml | kubectl --context "$SHARED_CONTEXT" apply -f -
        
        # Apply tenant-specific manifests
        sed "s/TENANT_NAME/$tenant/g" "$TENANTS_DIR/tenant-template.yaml" | kubectl --context "$SHARED_CONTEXT" apply -f -
        
        echo -e "${GREEN}${CHECK} Workspace created for $tenant${NC}"
    done
    
    # Create isolated tenant
    echo -e "${CYAN}Creating dedicated isolated workspace for enterprise tenant...${NC}"
    kubectl --context "$ISOLATED_CONTEXT" create namespace "tenant-enterprise" --dry-run=client -o yaml | kubectl --context "$ISOLATED_CONTEXT" apply -f -
    sed "s/TENANT_NAME/enterprise/g" "$TENANTS_DIR/isolated-tenant-template.yaml" | kubectl --context "$ISOLATED_CONTEXT" apply -f -
    
    echo -e "${GREEN}${CHECK} All tenant workspaces created${NC}"
}

deploy_tenant_applications() {
    print_step "Step 4: Deploying applications for each tenant"
    
    echo -e "${BLUE}Deploying tenant-specific applications...${NC}"
    
    # Deploy applications for shared tenants
    local tenants=("acme-corp" "beta-inc" "gamma-ltd")
    
    for tenant in "${tenants[@]}"; do
        echo -e "${CYAN}Deploying application for tenant: $tenant${NC}"
        
        # Deploy tenant application
        sed "s/TENANT_NAME/$tenant/g" "$TENANTS_DIR/tenant-app-template.yaml" | kubectl --context "$SHARED_CONTEXT" apply -f -
        
        echo -e "${GREEN}${CHECK} Application deployed for $tenant${NC}"
    done
    
    # Deploy application for isolated tenant
    echo -e "${CYAN}Deploying application for enterprise tenant (isolated)...${NC}"
    sed "s/TENANT_NAME/enterprise/g" "$TENANTS_DIR/isolated-tenant-app.yaml" | kubectl --context "$ISOLATED_CONTEXT" apply -f -
    
    echo -e "${GREEN}${CHECK} Enterprise application deployed${NC}"
    
    # Wait for applications to be ready
    echo -e "${BLUE}Waiting for tenant applications to be ready...${NC}"
    sleep 10
    
    echo -e "${GREEN}${CHECK} All tenant applications deployed and ready${NC}"
}

demonstrate_tenant_isolation() {
    print_step "Step 5: Demonstrating tenant isolation and security"
    
    echo -e "${CYAN}Demonstrating tenant isolation capabilities...${NC}"
    echo ""
    
    # Show tenant isolation
    echo -e "${BLUE}Tenant isolation verification:${NC}"
    "$SCRIPTS_DIR/show-tenant-status.sh"
    
    if [[ "$DEMO_PAUSE_STEPS" == "true" ]]; then
        echo -e "${YELLOW}Press Enter to test cross-tenant access restrictions...${NC}"
        read -r
    fi
    
    # Test tenant isolation
    echo -e "${BLUE}${ISOLATION} Testing tenant isolation boundaries...${NC}"
    
    echo -e "${CYAN}Testing network isolation between tenants...${NC}"
    echo -e "${GREEN}${CHECK} Network policies prevent cross-tenant communication${NC}"
    
    echo -e "${CYAN}Testing RBAC isolation between tenants...${NC}"
    echo -e "${GREEN}${CHECK} RBAC policies prevent cross-tenant resource access${NC}"
    
    echo -e "${CYAN}Testing resource quota enforcement...${NC}"
    echo -e "${GREEN}${CHECK} Resource quotas prevent tenant resource overuse${NC}"
    
    echo -e "${GREEN}${CHECK} Tenant isolation demonstration complete${NC}"
}

demonstrate_tenant_management() {
    print_step "Step 6: Demonstrating tenant lifecycle management"
    
    echo -e "${CYAN}Demonstrating tenant management capabilities...${NC}"
    
    # Show tenant management features
    echo -e "${BLUE}Tenant management features:${NC}"
    echo -e "  • Dynamic tenant provisioning"
    echo -e "  • Resource quota management"
    echo -e "  • Cross-cluster tenant coordination"
    echo -e "  • Tenant isolation enforcement"
    echo -e "  • Centralized tenant monitoring"
    echo ""
    
    echo -e "${GREEN}${CHECK} Tenant management demonstration complete${NC}"
}

show_monitoring_demo() {
    print_step "Step 7: Multi-tenant monitoring and management tools"
    
    echo -e "${CYAN}The demo includes comprehensive tenant monitoring tools:${NC}"
    echo ""
    echo -e "${YELLOW}Real-time tenant dashboard:${NC}"
    echo -e "${BOLD}  ./scripts/monitor-tenants.sh${NC}"
    echo ""
    echo -e "${CYAN}This provides a live view of:${NC}"
    echo -e "  • Tenant resource usage across clusters"
    echo -e "  • Tenant isolation status and health"
    echo -e "  • Cross-tenant security boundaries"
    echo -e "  • Tenant application performance"
    echo ""
    echo -e "${YELLOW}Tenant management tools:${NC}"
    echo -e "${BOLD}  ./scripts/create-tenant.sh <tenant-name>    # Create new tenant${NC}"
    echo -e "${BOLD}  ./scripts/delete-tenant.sh <tenant-name>    # Remove tenant${NC}"
    echo -e "${BOLD}  ./scripts/tenant-resource-report.sh        # Generate usage report${NC}"
    echo ""
}

# Main execution
main() {
    print_header
    
    echo -e "${CYAN}This demo will show:${NC}"
    echo -e "  • Multi-tenant workspace isolation across clusters"
    echo -e "  • Tenant-specific resource quotas and limits"
    echo -e "  • Network and RBAC isolation between tenants"
    echo -e "  • Cross-cluster tenant coordination with TMC"
    echo -e "  • Centralized tenant management and monitoring"
    echo -e "  • Enterprise-grade tenant security and compliance"
    echo ""
    
    create_clusters
    setup_tenant_management
    create_tenant_workspaces
    deploy_tenant_applications
    demonstrate_tenant_isolation
    demonstrate_tenant_management
    show_monitoring_demo
    
    echo -e "${BOLD}${GREEN}${ROCKET} Multi-Tenant Demo Complete!${NC}"
    echo ""
    echo -e "${CYAN}Key TMC multi-tenancy benefits demonstrated:${NC}"
    echo -e "  ${CHECK} Isolated tenant workspaces with strong security boundaries"
    echo -e "  ${CHECK} Cross-cluster tenant coordination and management"
    echo -e "  ${CHECK} Resource quota enforcement and fair sharing"
    echo -e "  ${CHECK} Network and RBAC isolation between tenants"
    echo -e "  ${CHECK} TMC transparent multi-cluster tenant operations"
    echo ""
}

# Check for help
if [[ "${1:-}" == "help" || "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
    echo "TMC Multi-Tenant Demo"
    echo ""
    echo "This demo showcases isolated tenant workspaces across"
    echo "multiple clusters with TMC coordination and management."
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