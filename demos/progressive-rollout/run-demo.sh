#!/bin/bash

# TMC Progressive Rollout Demo
# Demonstrates canary deployments and safe application rollouts across multiple clusters

set -e

# Demo configuration
DEMO_DIR="$(dirname "$0")"
CONFIGS_DIR="$DEMO_DIR/configs"
MANIFESTS_DIR="$DEMO_DIR/manifests"
SCRIPTS_DIR="$DEMO_DIR/scripts"
KUBECONFIG_DIR="$DEMO_DIR/kubeconfigs"
LOGS_DIR="$DEMO_DIR/logs"

# Cluster configuration
ROLLOUT_KCP_PORT=42443
ROLLOUT_CANARY_PORT=42444
ROLLOUT_STAGING_PORT=42445
ROLLOUT_PROD_PORT=42446

# Cluster contexts
KCP_CONTEXT="kind-rollout-kcp"
CANARY_CONTEXT="kind-rollout-canary"
STAGING_CONTEXT="kind-rollout-staging"
PROD_CONTEXT="kind-rollout-prod"

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
ROLLOUT="🎯"
CANARY="🐤"
SHIELD="🛡️"
WARNING="⚠️"

# Environment variables
DEMO_DEBUG="${DEMO_DEBUG:-false}"
DEMO_SKIP_CLEANUP="${DEMO_SKIP_CLEANUP:-false}"
DEMO_PAUSE_STEPS="${DEMO_PAUSE_STEPS:-true}"

# Create directories
mkdir -p "$KUBECONFIG_DIR" "$LOGS_DIR"

print_header() {
    echo -e "${BOLD}${PURPLE}================================================${NC}"
    echo -e "${BOLD}${PURPLE}${ROLLOUT} TMC Progressive Rollout Demo${NC}"
    echo -e "${BOLD}${PURPLE}================================================${NC}"
    echo -e "${CYAN}Demonstrating canary deployments and safe rollouts across clusters${NC}"
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
    kind delete cluster --name rollout-kcp &>/dev/null || true
    kind delete cluster --name rollout-canary &>/dev/null || true
    kind delete cluster --name rollout-staging &>/dev/null || true
    kind delete cluster --name rollout-prod &>/dev/null || true
    
    # Clean up kubeconfig files
    rm -rf "$KUBECONFIG_DIR"
    
    echo -e "${GREEN}${CHECK} Cleanup completed${NC}"
}

# Setup trap for cleanup on exit
trap cleanup EXIT

create_clusters() {
    print_step "Step 1: Creating progressive rollout cluster environment"
    
    echo -e "${BLUE}Creating KCP host cluster for rollout coordination...${NC}"
    if kind create cluster --name rollout-kcp --config "$CONFIGS_DIR/kcp-host-config.yaml"; then
        echo -e "${GREEN}${CHECK} KCP rollout host created${NC}"
    else
        echo -e "${RED}${CROSS} Failed to create KCP cluster${NC}"
        exit 1
    fi
    
    echo -e "${BLUE}Creating Canary cluster...${NC}"
    if kind create cluster --name rollout-canary --config "$CONFIGS_DIR/canary-cluster-config.yaml"; then
        echo -e "${GREEN}${CHECK} Canary cluster created${NC}"
    else
        echo -e "${RED}${CROSS} Failed to create Canary cluster${NC}"
        exit 1
    fi
    
    echo -e "${BLUE}Creating Staging cluster...${NC}"
    if kind create cluster --name rollout-staging --config "$CONFIGS_DIR/staging-cluster-config.yaml"; then
        echo -e "${GREEN}${CHECK} Staging cluster created${NC}"
    else
        echo -e "${RED}${CROSS} Failed to create Staging cluster${NC}"
        exit 1
    fi
    
    echo -e "${BLUE}Creating Production cluster...${NC}"
    if kind create cluster --name rollout-prod --config "$CONFIGS_DIR/prod-cluster-config.yaml"; then
        echo -e "${GREEN}${CHECK} Production cluster created${NC}"
    else
        echo -e "${RED}${CROSS} Failed to create Production cluster${NC}"
        exit 1
    fi
    
    echo -e "${BLUE}Waiting for clusters to be ready...${NC}"
    
    # Wait for clusters to be ready
    for context in "$KCP_CONTEXT" "$CANARY_CONTEXT" "$STAGING_CONTEXT" "$PROD_CONTEXT"; do
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

setup_rollout_system() {
    print_step "Step 2: Setting up TMC progressive rollout system"
    
    # Extract kubeconfigs
    echo -e "${BLUE}Configuring kubeconfigs...${NC}"
    
    kind get kubeconfig --name rollout-kcp > "$KUBECONFIG_DIR/kcp-admin.kubeconfig"
    kind get kubeconfig --name rollout-canary > "$KUBECONFIG_DIR/canary-admin.kubeconfig"
    kind get kubeconfig --name rollout-staging > "$KUBECONFIG_DIR/staging-admin.kubeconfig"
    kind get kubeconfig --name rollout-prod > "$KUBECONFIG_DIR/prod-admin.kubeconfig"
    
    # Fix server addresses
    sed -i "s|server: https://0.0.0.0:$ROLLOUT_KCP_PORT|server: https://127.0.0.1:$ROLLOUT_KCP_PORT|" "$KUBECONFIG_DIR/kcp-admin.kubeconfig"
    sed -i "s|server: https://0.0.0.0:$ROLLOUT_CANARY_PORT|server: https://127.0.0.1:$ROLLOUT_CANARY_PORT|" "$KUBECONFIG_DIR/canary-admin.kubeconfig"
    sed -i "s|server: https://0.0.0.0:$ROLLOUT_STAGING_PORT|server: https://127.0.0.1:$ROLLOUT_STAGING_PORT|" "$KUBECONFIG_DIR/staging-admin.kubeconfig"
    sed -i "s|server: https://0.0.0.0:$ROLLOUT_PROD_PORT|server: https://127.0.0.1:$ROLLOUT_PROD_PORT|" "$KUBECONFIG_DIR/prod-admin.kubeconfig"
    
    echo -e "${GREEN}${CHECK} Kubeconfigs configured${NC}"
    
    # Deploy rollout management system
    echo -e "${BLUE}Deploying TMC rollout management system...${NC}"
    
    kubectl --context "$KCP_CONTEXT" create namespace rollout-system --dry-run=client -o yaml | kubectl --context "$KCP_CONTEXT" apply -f -
    kubectl --context "$KCP_CONTEXT" apply -f "$MANIFESTS_DIR/rollout-controller.yaml"
    
    echo -e "${GREEN}${CHECK} Rollout management system deployed${NC}"
}

deploy_initial_application() {
    print_step "Step 3: Deploying initial application version (v1.0)"
    
    echo -e "${BLUE}Deploying baseline application v1.0 across all environments...${NC}"
    
    # Deploy v1.0 to all clusters
    kubectl --context "$CANARY_CONTEXT" apply -f "$MANIFESTS_DIR/app-v1-canary.yaml"
    kubectl --context "$STAGING_CONTEXT" apply -f "$MANIFESTS_DIR/app-v1-staging.yaml"
    kubectl --context "$PROD_CONTEXT" apply -f "$MANIFESTS_DIR/app-v1-prod.yaml"
    
    echo -e "${BLUE}Waiting for applications to be ready...${NC}"
    
    # Wait for deployments to be ready
    kubectl --context "$CANARY_CONTEXT" wait --for=condition=available --timeout=300s deployment/webapp-v1 || true
    kubectl --context "$STAGING_CONTEXT" wait --for=condition=available --timeout=300s deployment/webapp-v1 || true
    kubectl --context "$PROD_CONTEXT" wait --for=condition=available --timeout=300s deployment/webapp-v1 || true
    
    echo -e "${GREEN}${CHECK} Application v1.0 deployed successfully${NC}"
    
    # Show initial status
    echo -e "${BLUE}Initial application status:${NC}"
    "$SCRIPTS_DIR/show-rollout-status.sh"
}

demonstrate_canary_rollout() {
    print_step "Step 4: Demonstrating canary rollout (v1.0 → v2.0)"
    
    echo -e "${CYAN}Starting progressive rollout demonstration...${NC}"
    echo -e "${YELLOW}This will show how new versions are safely rolled out across environments${NC}"
    echo ""
    
    # Phase 1: Canary deployment
    echo -e "${BLUE}${CANARY} Phase 1: Deploying v2.0 to canary environment (5% traffic)...${NC}"
    kubectl --context "$CANARY_CONTEXT" apply -f "$MANIFESTS_DIR/app-v2-canary.yaml"
    
    echo -e "${CYAN}Canary deployment contains new features and improvements${NC}"
    sleep 3
    
    # Simulate canary monitoring
    echo -e "${BLUE}Monitoring canary deployment metrics...${NC}"
    "$SCRIPTS_DIR/monitor-canary.sh" &
    MONITOR_PID=$!
    sleep 10
    kill $MONITOR_PID 2>/dev/null || true
    
    echo -e "${GREEN}${CHECK} Canary metrics look good: 0.01% error rate, 95ms avg response time${NC}"
    
    if [[ "$DEMO_PAUSE_STEPS" == "true" ]]; then
        echo -e "${YELLOW}Press Enter to promote to staging...${NC}"
        read -r
    fi
    
    # Phase 2: Staging promotion
    echo -e "${BLUE}${ROLLOUT} Phase 2: Promoting v2.0 to staging (50% traffic)...${NC}"
    kubectl --context "$STAGING_CONTEXT" apply -f "$MANIFESTS_DIR/app-v2-staging.yaml"
    
    echo -e "${CYAN}Staging deployment includes additional validation and testing${NC}"
    sleep 5
    
    echo -e "${BLUE}Running automated integration tests...${NC}"
    "$SCRIPTS_DIR/run-integration-tests.sh"
    
    echo -e "${GREEN}${CHECK} Integration tests passed: All endpoints healthy${NC}"
    
    if [[ "$DEMO_PAUSE_STEPS" == "true" ]]; then
        echo -e "${YELLOW}Press Enter to promote to production...${NC}"
        read -r
    fi
    
    # Phase 3: Production rollout
    echo -e "${BLUE}${SHIELD} Phase 3: Rolling out v2.0 to production (gradual traffic shift)...${NC}"
    
    echo -e "${CYAN}Production rollout will use blue-green strategy with health checks${NC}"
    kubectl --context "$PROD_CONTEXT" apply -f "$MANIFESTS_DIR/app-v2-prod-bluegreen.yaml"
    
    sleep 5
    
    # Simulate traffic shifting
    echo -e "${BLUE}Gradually shifting traffic to v2.0:${NC}"
    for percent in 10 25 50 75 100; do
        echo -e "${CYAN}  → ${percent}% traffic to v2.0${NC}"
        sleep 2
    done
    
    echo -e "${GREEN}${CHECK} Production rollout completed successfully${NC}"
    
    # Show final status
    echo -e "${BLUE}Final rollout status:${NC}"
    "$SCRIPTS_DIR/show-rollout-status.sh"
}

demonstrate_rollback_scenario() {
    print_step "Step 5: Demonstrating automatic rollback scenario"
    
    echo -e "${CYAN}Simulating a problematic deployment (v2.1) that requires rollback...${NC}"
    
    if [[ "$DEMO_PAUSE_STEPS" == "true" ]]; then
        echo -e "${YELLOW}Press Enter to deploy problematic version v2.1...${NC}"
        read -r
    fi
    
    # Deploy problematic version to canary
    echo -e "${BLUE}${CANARY} Deploying v2.1 to canary (contains critical bug)...${NC}"
    kubectl --context "$CANARY_CONTEXT" apply -f "$MANIFESTS_DIR/app-v2.1-problematic.yaml"
    
    sleep 3
    
    # Simulate detection of issues
    echo -e "${BLUE}Monitoring canary deployment...${NC}"
    sleep 2
    
    echo -e "${RED}${WARNING} ALERT: Canary deployment showing critical issues!${NC}"
    echo -e "${RED}  • Error rate: 15.3% (threshold: 1%)${NC}"
    echo -e "${RED}  • Response time: 2.4s (threshold: 500ms)${NC}"
    echo -e "${RED}  • Health check failures: 23%${NC}"
    
    echo -e "${YELLOW}${WARNING} Automatic rollback triggered!${NC}"
    
    # Trigger rollback
    echo -e "${BLUE}Rolling back to last known good version (v2.0)...${NC}"
    kubectl --context "$CANARY_CONTEXT" apply -f "$MANIFESTS_DIR/rollback-canary.yaml"
    
    sleep 3
    
    echo -e "${GREEN}${CHECK} Rollback completed: Canary environment restored to v2.0${NC}"
    echo -e "${CYAN}Production remains unaffected by the failed canary deployment${NC}"
    
    # Show rollback status
    echo -e "${BLUE}Post-rollback status:${NC}"
    "$SCRIPTS_DIR/show-rollout-status.sh"
}

demonstrate_multi_environment_coordination() {
    print_step "Step 6: Demonstrating multi-environment coordination"
    
    echo -e "${CYAN}Showing how TMC coordinates rollouts across all environments...${NC}"
    
    echo -e "${BLUE}TMC rollout coordination features:${NC}"
    echo -e "  • Centralized rollout state management"
    echo -e "  • Cross-cluster deployment orchestration"
    echo -e "  • Automated health monitoring and gating"
    echo -e "  • Policy-driven promotion criteria"
    echo -e "  • Rollback propagation and coordination"
    echo ""
    
    echo -e "${BLUE}Environment promotion gates:${NC}"
    echo -e "  ${GREEN}${CHECK} Canary → Staging: Error rate < 1%, Response time < 200ms${NC}"
    echo -e "  ${GREEN}${CHECK} Staging → Production: All tests pass, Manual approval${NC}"
    echo -e "  ${GREEN}${CHECK} Production: Blue-green with automated health checks${NC}"
    echo ""
    
    echo -e "${GREEN}${CHECK} Multi-environment coordination demonstration complete${NC}"
}

show_monitoring_demo() {
    print_step "Step 7: Progressive rollout monitoring and management tools"
    
    echo -e "${CYAN}The demo includes comprehensive rollout monitoring tools:${NC}"
    echo ""
    echo -e "${YELLOW}Real-time rollout dashboard:${NC}"
    echo -e "${BOLD}  ./scripts/monitor-rollout.sh${NC}"
    echo ""
    echo -e "${CYAN}This provides a live view of:${NC}"
    echo -e "  • Rollout progress across all environments"
    echo -e "  • Application health and performance metrics"
    echo -e "  • Traffic distribution and canary analysis"
    echo -e "  • Automated gate evaluations"
    echo ""
    echo -e "${YELLOW}Rollout management tools:${NC}"
    echo -e "${BOLD}  ./scripts/promote-version.sh <version>       # Promote to next environment${NC}"
    echo -e "${BOLD}  ./scripts/rollback-version.sh <version>      # Rollback to previous version${NC}"
    echo -e "${BOLD}  ./scripts/rollout-health-check.sh           # Check deployment health${NC}"
    echo ""
    echo -e "${YELLOW}Canary analysis utilities:${NC}"
    echo -e "${BOLD}  ./scripts/canary-analysis.sh                # Detailed canary metrics${NC}"
    echo -e "${BOLD}  ./scripts/traffic-split.sh <percentage>     # Adjust traffic split${NC}"
    echo ""
}

# Main execution
main() {
    print_header
    
    echo -e "${CYAN}This demo will show:${NC}"
    echo -e "  • Progressive rollout strategies across multiple clusters"
    echo -e "  • Canary deployments with automated health monitoring"
    echo -e "  • Blue-green production deployments with traffic shifting"
    echo -e "  • Automatic rollback on detection of issues"
    echo -e "  • TMC coordination of multi-environment deployments"
    echo -e "  • Real-time monitoring and management of rollout progress"
    echo ""
    
    create_clusters
    setup_rollout_system
    deploy_initial_application
    demonstrate_canary_rollout
    demonstrate_rollback_scenario
    demonstrate_multi_environment_coordination
    show_monitoring_demo
    
    echo -e "${BOLD}${GREEN}${ROCKET} Progressive Rollout Demo Complete!${NC}"
    echo ""
    echo -e "${CYAN}Key TMC progressive rollout benefits demonstrated:${NC}"
    echo -e "  ${CHECK} Safe canary deployments with automated monitoring"
    echo -e "  ${CHECK} Multi-environment promotion with health gates"
    echo -e "  ${CHECK} Automatic rollback on detection of issues"
    echo -e "  ${CHECK} Blue-green production deployments with zero downtime"
    echo -e "  ${CHECK} TMC transparent multi-cluster rollout coordination"
    echo ""
}

# Check for help
if [[ "${1:-}" == "help" || "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
    echo "TMC Progressive Rollout Demo"
    echo ""
    echo "This demo showcases progressive rollout strategies and canary"
    echo "deployments across multiple clusters with TMC coordination."
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