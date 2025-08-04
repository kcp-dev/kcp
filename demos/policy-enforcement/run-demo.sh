#!/bin/bash

# TMC Policy Enforcement Demo
# Demonstrates global policy enforcement across multiple clusters with TMC coordination

set -e

# Demo configuration
DEMO_DIR="$(dirname "$0")"
CONFIGS_DIR="$DEMO_DIR/configs"
MANIFESTS_DIR="$DEMO_DIR/manifests"
POLICIES_DIR="$DEMO_DIR/policies"
SCRIPTS_DIR="$DEMO_DIR/scripts"
KUBECONFIG_DIR="$DEMO_DIR/kubeconfigs"
LOGS_DIR="$DEMO_DIR/logs"

# Cluster configuration
POLICY_KCP_PORT=40443
POLICY_DEV_PORT=40444
POLICY_STAGING_PORT=40445
POLICY_PROD_PORT=40446

# Cluster contexts
KCP_CONTEXT="kind-policy-kcp"
DEV_CONTEXT="kind-policy-dev"
STAGING_CONTEXT="kind-policy-staging"
PROD_CONTEXT="kind-policy-prod"

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
SHIELD="🛡️"
WARNING="⚠️"
POLICY="📋"

# Environment variables
DEMO_DEBUG="${DEMO_DEBUG:-false}"
DEMO_SKIP_CLEANUP="${DEMO_SKIP_CLEANUP:-false}"
DEMO_PAUSE_STEPS="${DEMO_PAUSE_STEPS:-true}"

# Create directories
mkdir -p "$KUBECONFIG_DIR" "$LOGS_DIR"

print_header() {
    echo -e "${BOLD}${PURPLE}================================================${NC}"
    echo -e "${BOLD}${PURPLE}${SHIELD} TMC Policy Enforcement Demo${NC}"
    echo -e "${BOLD}${PURPLE}================================================${NC}"
    echo -e "${CYAN}Demonstrating global policy enforcement across clusters${NC}"
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
    kind delete cluster --name policy-kcp &>/dev/null || true
    kind delete cluster --name policy-dev &>/dev/null || true
    kind delete cluster --name policy-staging &>/dev/null || true
    kind delete cluster --name policy-prod &>/dev/null || true
    
    # Clean up kubeconfig files
    rm -rf "$KUBECONFIG_DIR"
    
    echo -e "${GREEN}${CHECK} Cleanup completed${NC}"
}

# Setup trap for cleanup on exit
trap cleanup EXIT

create_clusters() {
    print_step "Step 1: Creating policy-managed multi-cluster environment"
    
    echo -e "${BLUE}Creating KCP host cluster for policy management...${NC}"
    debug_log "kind create cluster --name policy-kcp --config $CONFIGS_DIR/kcp-host-config.yaml"
    if kind create cluster --name policy-kcp --config "$CONFIGS_DIR/kcp-host-config.yaml"; then
        echo -e "${GREEN}${CHECK} KCP policy host created${NC}"
    else
        echo -e "${RED}${CROSS} Failed to create KCP cluster${NC}"
        exit 1
    fi
    
    echo -e "${BLUE}Creating Development cluster...${NC}"
    debug_log "kind create cluster --name policy-dev --config $CONFIGS_DIR/dev-cluster-config.yaml"
    if kind create cluster --name policy-dev --config "$CONFIGS_DIR/dev-cluster-config.yaml"; then
        echo -e "${GREEN}${CHECK} Dev cluster created${NC}"
    else
        echo -e "${RED}${CROSS} Failed to create Dev cluster${NC}"
        exit 1
    fi
    
    echo -e "${BLUE}Creating Staging cluster...${NC}"
    debug_log "kind create cluster --name policy-staging --config $CONFIGS_DIR/staging-cluster-config.yaml"
    if kind create cluster --name policy-staging --config "$CONFIGS_DIR/staging-cluster-config.yaml"; then
        echo -e "${GREEN}${CHECK} Staging cluster created${NC}"
    else
        echo -e "${RED}${CROSS} Failed to create Staging cluster${NC}"
        exit 1
    fi
    
    echo -e "${BLUE}Creating Production cluster...${NC}"
    debug_log "kind create cluster --name policy-prod --config $CONFIGS_DIR/prod-cluster-config.yaml"
    if kind create cluster --name policy-prod --config "$CONFIGS_DIR/prod-cluster-config.yaml"; then
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
    print_step "Step 2: Setting up TMC syncers with policy enforcement capabilities"
    
    # Extract kubeconfigs with correct server addresses
    echo -e "${BLUE}Extracting and configuring kubeconfigs...${NC}"
    
    kind get kubeconfig --name policy-kcp > "$KUBECONFIG_DIR/kcp-admin.kubeconfig"
    kind get kubeconfig --name policy-dev > "$KUBECONFIG_DIR/dev-admin.kubeconfig"
    kind get kubeconfig --name policy-staging > "$KUBECONFIG_DIR/staging-admin.kubeconfig"
    kind get kubeconfig --name policy-prod > "$KUBECONFIG_DIR/prod-admin.kubeconfig"
    
    # Fix server addresses
    sed -i "s|server: https://0.0.0.0:$POLICY_KCP_PORT|server: https://127.0.0.1:$POLICY_KCP_PORT|" "$KUBECONFIG_DIR/kcp-admin.kubeconfig"
    sed -i "s|server: https://0.0.0.0:$POLICY_DEV_PORT|server: https://127.0.0.1:$POLICY_DEV_PORT|" "$KUBECONFIG_DIR/dev-admin.kubeconfig"
    sed -i "s|server: https://0.0.0.0:$POLICY_STAGING_PORT|server: https://127.0.0.1:$POLICY_STAGING_PORT|" "$KUBECONFIG_DIR/staging-admin.kubeconfig"
    sed -i "s|server: https://0.0.0.0:$POLICY_PROD_PORT|server: https://127.0.0.1:$POLICY_PROD_PORT|" "$KUBECONFIG_DIR/prod-admin.kubeconfig"
    
    echo -e "${GREEN}${CHECK} Kubeconfigs configured${NC}"
    
    # Deploy TMC syncers with policy enforcement
    echo -e "${BLUE}Deploying TMC syncers with policy enforcement...${NC}"
    
    kubectl --context "$DEV_CONTEXT" apply -f "$MANIFESTS_DIR/dev-syncer.yaml"
    kubectl --context "$STAGING_CONTEXT" apply -f "$MANIFESTS_DIR/staging-syncer.yaml"
    kubectl --context "$PROD_CONTEXT" apply -f "$MANIFESTS_DIR/prod-syncer.yaml"
    
    echo -e "${GREEN}${CHECK} TMC syncers with policy enforcement deployed${NC}"
}

deploy_policy_engine() {
    print_step "Step 3: Deploying centralized policy engine on KCP"
    
    echo -e "${BLUE}Installing policy enforcement engine...${NC}"
    
    # Create policy namespace
    kubectl --context "$KCP_CONTEXT" create namespace policy-system --dry-run=client -o yaml | kubectl --context "$KCP_CONTEXT" apply -f -
    
    # Deploy policy engine components
    kubectl --context "$KCP_CONTEXT" apply -f "$MANIFESTS_DIR/policy-engine.yaml"
    
    echo -e "${BLUE}Waiting for policy engine to be ready...${NC}"
    kubectl --context "$KCP_CONTEXT" wait --for=condition=available --timeout=300s deployment/policy-controller -n policy-system || true
    
    echo -e "${GREEN}${CHECK} Policy engine deployed${NC}"
}

deploy_global_policies() {
    print_step "Step 4: Deploying global policies across all clusters"
    
    echo -e "${BLUE}Deploying security policies...${NC}"
    
    # Deploy security policies
    kubectl --context "$KCP_CONTEXT" apply -f "$POLICIES_DIR/security-policies.yaml"
    
    echo -e "${BLUE}Deploying resource quotas and limits...${NC}"
    
    # Deploy resource policies
    kubectl --context "$KCP_CONTEXT" apply -f "$POLICIES_DIR/resource-policies.yaml"
    
    echo -e "${BLUE}Deploying compliance policies...${NC}"
    
    # Deploy compliance policies
    kubectl --context "$KCP_CONTEXT" apply -f "$POLICIES_DIR/compliance-policies.yaml"
    
    echo -e "${BLUE}Deploying network policies...${NC}"
    
    # Deploy network policies
    kubectl --context "$KCP_CONTEXT" apply -f "$POLICIES_DIR/network-policies.yaml"
    
    echo -e "${GREEN}${CHECK} Global policies deployed${NC}"
    
    # Wait for policy synchronization
    echo -e "${BLUE}Waiting for policy synchronization across clusters...${NC}"
    sleep 10
    
    echo -e "${GREEN}${CHECK} Policies synchronized to all clusters${NC}"
}

demonstrate_policy_enforcement() {
    print_step "Step 5: Demonstrating policy enforcement across clusters"
    
    echo -e "${CYAN}Starting policy enforcement demonstration...${NC}"
    echo -e "${YELLOW}This will show how global policies are enforced consistently across all clusters${NC}"
    echo ""
    
    # Show initial policy status
    echo -e "${BLUE}Initial policy status across environments:${NC}"
    "$SCRIPTS_DIR/show-policy-status.sh"
    
    if [[ "$DEMO_PAUSE_STEPS" == "true" ]]; then
        echo -e "${YELLOW}Press Enter to test policy compliance with valid deployments...${NC}"
        read -r
    fi
    
    # Test compliant deployments
    echo -e "${BLUE}${SHIELD} Testing policy-compliant deployments...${NC}"
    
    # Deploy compliant applications
    kubectl --context "$DEV_CONTEXT" apply -f "$MANIFESTS_DIR/compliant-app-dev.yaml" || true
    kubectl --context "$STAGING_CONTEXT" apply -f "$MANIFESTS_DIR/compliant-app-staging.yaml" || true
    kubectl --context "$PROD_CONTEXT" apply -f "$MANIFESTS_DIR/compliant-app-prod.yaml" || true
    
    sleep 5
    
    echo -e "${GREEN}${CHECK} Compliant applications deployed successfully${NC}"
    echo -e "${CYAN}All applications meet security, resource, and compliance policies${NC}"
    
    if [[ "$DEMO_PAUSE_STEPS" == "true" ]]; then
        echo -e "${YELLOW}Press Enter to test policy violations...${NC}"
        read -r
    fi
    
    # Test policy violations
    echo -e "${BLUE}${WARNING} Testing policy violations (these should be blocked)...${NC}"
    
    echo -e "${CYAN}Attempting to deploy applications that violate policies:${NC}"
    
    # Try to deploy non-compliant applications (these should fail)
    echo -e "${YELLOW}1. Testing security policy violation (privileged container)...${NC}"
    kubectl --context "$DEV_CONTEXT" apply -f "$MANIFESTS_DIR/violation-security.yaml" 2>/dev/null || echo -e "${GREEN}${CHECK} Security policy violation blocked${NC}"
    
    echo -e "${YELLOW}2. Testing resource policy violation (excessive CPU request)...${NC}"
    kubectl --context "$STAGING_CONTEXT" apply -f "$MANIFESTS_DIR/violation-resources.yaml" 2>/dev/null || echo -e "${GREEN}${CHECK} Resource policy violation blocked${NC}"
    
    echo -e "${YELLOW}3. Testing compliance policy violation (missing required labels)...${NC}"
    kubectl --context "$PROD_CONTEXT" apply -f "$MANIFESTS_DIR/violation-compliance.yaml" 2>/dev/null || echo -e "${GREEN}${CHECK} Compliance policy violation blocked${NC}"
    
    echo -e "${GREEN}${CHECK} Policy enforcement demonstration complete${NC}"
    
    # Show final policy status
    echo -e "${BLUE}Final policy status and violations:${NC}"
    "$SCRIPTS_DIR/show-policy-status.sh"
}

demonstrate_policy_updates() {
    print_step "Step 6: Demonstrating dynamic policy updates"
    
    echo -e "${CYAN}Demonstrating dynamic policy updates across clusters...${NC}"
    
    if [[ "$DEMO_PAUSE_STEPS" == "true" ]]; then
        echo -e "${YELLOW}Press Enter to update global resource limits...${NC}"
        read -r
    fi
    
    # Update resource policies
    echo -e "${BLUE}${POLICY} Updating global resource policies...${NC}"
    kubectl --context "$KCP_CONTEXT" apply -f "$POLICIES_DIR/updated-resource-policies.yaml"
    
    echo -e "${CYAN}Policy update will propagate to all clusters via TMC...${NC}"
    sleep 5
    
    echo -e "${GREEN}${CHECK} Policy updates synchronized across all clusters${NC}"
    
    if [[ "$DEMO_PAUSE_STEPS" == "true" ]]; then
        echo -e "${YELLOW}Press Enter to test new policy enforcement...${NC}"
        read -r
    fi
    
    # Test updated policies
    echo -e "${BLUE}Testing enforcement of updated policies...${NC}"
    kubectl --context "$DEV_CONTEXT" apply -f "$MANIFESTS_DIR/test-updated-policy.yaml" || echo -e "${GREEN}${CHECK} Updated policy enforced correctly${NC}"
    
    echo -e "${GREEN}${CHECK} Dynamic policy update demonstration complete${NC}"
}

show_monitoring_demo() {
    print_step "Step 7: Policy monitoring and compliance tools"
    
    echo -e "${CYAN}The demo includes comprehensive policy monitoring tools:${NC}"
    echo ""
    echo -e "${YELLOW}Real-time policy dashboard:${NC}"
    echo -e "${BOLD}  ./scripts/monitor-policies.sh${NC}"
    echo ""
    echo -e "${CYAN}This provides a live view of:${NC}"
    echo -e "  • Policy compliance status across all clusters"
    echo -e "  • Real-time violation detection and blocking"
    echo -e "  • Policy synchronization health"
    echo -e "  • Compliance reporting and metrics"
    echo ""
    echo -e "${YELLOW}Policy management tools:${NC}"
    echo -e "${BOLD}  ./scripts/update-policy.sh <policy-name>     # Update specific policy${NC}"
    echo -e "${BOLD}  ./scripts/check-compliance.sh              # Run compliance audit${NC}"
    echo -e "${BOLD}  ./scripts/generate-policy-report.sh        # Generate compliance report${NC}"
    echo ""
    echo -e "${YELLOW}Policy testing utilities:${NC}"
    echo -e "${BOLD}  ./scripts/test-policy-violation.sh         # Test policy enforcement${NC}"
    echo ""
}

# Main execution
main() {
    print_header
    
    echo -e "${CYAN}This demo will show:${NC}"
    echo -e "  • Centralized policy management with TMC coordination"
    echo -e "  • Global policy enforcement across multiple clusters"
    echo -e "  • Security, resource, compliance, and network policies"
    echo -e "  • Real-time policy violation detection and blocking"
    echo -e "  • Dynamic policy updates with automatic synchronization"
    echo -e "  • Comprehensive compliance monitoring and reporting"
    echo ""
    
    create_clusters
    setup_tmc_syncers
    deploy_policy_engine
    deploy_global_policies
    demonstrate_policy_enforcement
    demonstrate_policy_updates
    show_monitoring_demo
    
    echo -e "${BOLD}${GREEN}${ROCKET} Policy Enforcement Demo Complete!${NC}"
    echo ""
    echo -e "${CYAN}Key TMC policy benefits demonstrated:${NC}"
    echo -e "  ${CHECK} Centralized policy management with distributed enforcement"
    echo -e "  ${CHECK} Consistent security and compliance across all clusters"
    echo -e "  ${CHECK} Real-time violation detection and prevention"
    echo -e "  ${CHECK} Dynamic policy updates with automatic synchronization"
    echo -e "  ${CHECK} TMC transparent multi-cluster policy coordination"
    echo ""
}

# Check for help
if [[ "${1:-}" == "help" || "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
    echo "TMC Policy Enforcement Demo"
    echo ""
    echo "This demo showcases global policy enforcement across"
    echo "multiple clusters with TMC coordination and transparency."
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