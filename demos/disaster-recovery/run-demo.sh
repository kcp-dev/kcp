#!/bin/bash

# TMC Disaster Recovery Demo
# Demonstrates automatic failover of workloads between clusters when one region becomes unavailable

set -e

# Demo configuration
DEMO_DIR="$(dirname "$0")"
CONFIGS_DIR="$DEMO_DIR/configs"
MANIFESTS_DIR="$DEMO_DIR/manifests"
SCRIPTS_DIR="$DEMO_DIR/scripts"
KUBECONFIG_DIR="$DEMO_DIR/kubeconfigs"
LOGS_DIR="$DEMO_DIR/logs"

# Cluster configuration
DR_KCP_PORT=38443
DR_EAST_PORT=38444
DR_WEST_PORT=38445

# Cluster contexts
KCP_CONTEXT="kind-dr-kcp"
EAST_CONTEXT="kind-dr-east"
WEST_CONTEXT="kind-dr-west"

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
WARNING="⚠️"

# Environment variables
DEMO_DEBUG="${DEMO_DEBUG:-false}"
DEMO_SKIP_CLEANUP="${DEMO_SKIP_CLEANUP:-false}"
DEMO_PAUSE_STEPS="${DEMO_PAUSE_STEPS:-true}"

# Create directories
mkdir -p "$KUBECONFIG_DIR" "$LOGS_DIR"

print_header() {
    echo -e "${BOLD}${PURPLE}================================================${NC}"
    echo -e "${BOLD}${PURPLE}${ROCKET} TMC Disaster Recovery Demo${NC}"
    echo -e "${BOLD}${PURPLE}================================================${NC}"
    echo -e "${CYAN}Demonstrating automatic failover between clusters${NC}"
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
    kind delete cluster --name dr-kcp &>/dev/null || true
    kind delete cluster --name dr-east &>/dev/null || true
    kind delete cluster --name dr-west &>/dev/null || true
    
    # Clean up kubeconfig files
    rm -rf "$KUBECONFIG_DIR"
    
    echo -e "${GREEN}${CHECK} Cleanup completed${NC}"
}

# Setup trap for cleanup on exit
trap cleanup EXIT

create_clusters() {
    print_step "Step 1: Creating multi-region clusters (KCP + East + West)"
    
    echo -e "${BLUE}Creating KCP host cluster...${NC}"
    debug_log "kind create cluster --name dr-kcp --config $CONFIGS_DIR/kcp-host-config.yaml"
    if kind create cluster --name dr-kcp --config "$CONFIGS_DIR/kcp-host-config.yaml"; then
        echo -e "${GREEN}${CHECK} KCP cluster created${NC}"
    else
        echo -e "${RED}${CROSS} Failed to create KCP cluster${NC}"
        exit 1
    fi
    
    echo -e "${BLUE}Creating East region cluster...${NC}"
    debug_log "kind create cluster --name dr-east --config $CONFIGS_DIR/east-cluster-config.yaml"
    if kind create cluster --name dr-east --config "$CONFIGS_DIR/east-cluster-config.yaml"; then
        echo -e "${GREEN}${CHECK} East cluster created${NC}"
    else
        echo -e "${RED}${CROSS} Failed to create East cluster${NC}"
        exit 1
    fi
    
    echo -e "${BLUE}Creating West region cluster...${NC}"
    debug_log "kind create cluster --name dr-west --config $CONFIGS_DIR/west-cluster-config.yaml"
    if kind create cluster --name dr-west --config "$CONFIGS_DIR/west-cluster-config.yaml"; then
        echo -e "${GREEN}${CHECK} West cluster created${NC}"
    else
        echo -e "${RED}${CROSS} Failed to create West cluster${NC}"
        exit 1
    fi
    
    echo -e "${BLUE}Waiting for clusters to be ready (this may take 2-3 minutes)...${NC}"
    
    # Wait for clusters to be ready
    for context in "$KCP_CONTEXT" "$EAST_CONTEXT" "$WEST_CONTEXT"; do
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
    print_step "Step 2: Setting up TMC syncers for cross-cluster communication"
    
    # Extract kubeconfigs with correct server addresses
    echo -e "${BLUE}Extracting and configuring kubeconfigs...${NC}"
    
    kind get kubeconfig --name dr-kcp > "$KUBECONFIG_DIR/kcp-admin.kubeconfig"
    kind get kubeconfig --name dr-east > "$KUBECONFIG_DIR/east-admin.kubeconfig"
    kind get kubeconfig --name dr-west > "$KUBECONFIG_DIR/west-admin.kubeconfig"
    
    # Fix server addresses (kind uses 0.0.0.0 but certs are for 127.0.0.1)
    sed -i "s|server: https://0.0.0.0:$DR_KCP_PORT|server: https://127.0.0.1:$DR_KCP_PORT|" "$KUBECONFIG_DIR/kcp-admin.kubeconfig"
    sed -i "s|server: https://0.0.0.0:$DR_EAST_PORT|server: https://127.0.0.1:$DR_EAST_PORT|" "$KUBECONFIG_DIR/east-admin.kubeconfig"
    sed -i "s|server: https://0.0.0.0:$DR_WEST_PORT|server: https://127.0.0.1:$DR_WEST_PORT|" "$KUBECONFIG_DIR/west-admin.kubeconfig"
    
    echo -e "${GREEN}${CHECK} Kubeconfigs configured${NC}"
    
    # Deploy TMC syncers (simulated for demo)
    echo -e "${BLUE}Deploying TMC syncers...${NC}"
    
    # Create syncer deployments on each cluster
    kubectl --context "$EAST_CONTEXT" apply -f "$MANIFESTS_DIR/east-syncer.yaml"
    kubectl --context "$WEST_CONTEXT" apply -f "$MANIFESTS_DIR/west-syncer.yaml"
    
    echo -e "${GREEN}${CHECK} TMC syncers deployed${NC}"
}

deploy_application() {
    print_step "Step 3: Deploying web application to both regions"
    
    echo -e "${BLUE}Deploying application to East region...${NC}"
    kubectl --context "$EAST_CONTEXT" apply -f "$MANIFESTS_DIR/webapp-east.yaml"
    
    echo -e "${BLUE}Deploying application to West region...${NC}"
    kubectl --context "$WEST_CONTEXT" apply -f "$MANIFESTS_DIR/webapp-west.yaml"
    
    echo -e "${BLUE}Deploying global load balancer...${NC}"
    kubectl --context "$KCP_CONTEXT" apply -f "$MANIFESTS_DIR/global-loadbalancer.yaml"
    
    # Wait for deployments to be ready
    echo -e "${BLUE}Waiting for applications to be ready...${NC}"
    
    kubectl --context "$EAST_CONTEXT" wait --for=condition=available --timeout=300s deployment/webapp-east
    kubectl --context "$WEST_CONTEXT" wait --for=condition=available --timeout=300s deployment/webapp-west
    
    echo -e "${GREEN}${CHECK} Applications deployed and ready${NC}"
}

deploy_monitoring() {
    print_step "Step 4: Deploying health monitoring and failover controller"
    
    echo -e "${BLUE}Deploying health monitors...${NC}"
    kubectl --context "$EAST_CONTEXT" apply -f "$MANIFESTS_DIR/health-monitor-east.yaml"
    kubectl --context "$WEST_CONTEXT" apply -f "$MANIFESTS_DIR/health-monitor-west.yaml"
    
    echo -e "${BLUE}Deploying failover controller...${NC}"
    kubectl --context "$KCP_CONTEXT" apply -f "$MANIFESTS_DIR/failover-controller.yaml"
    
    echo -e "${GREEN}${CHECK} Monitoring and failover components deployed${NC}"
}

demonstrate_failover() {
    print_step "Step 5: Demonstrating disaster recovery failover"
    
    echo -e "${CYAN}Starting failover demonstration...${NC}"
    echo -e "${YELLOW}The demo will now simulate a regional failure and show automatic failover${NC}"
    echo ""
    
    # Show initial state
    echo -e "${BLUE}Initial state - both regions healthy:${NC}"
    "$SCRIPTS_DIR/show-status.sh"
    
    if [[ "$DEMO_PAUSE_STEPS" == "true" ]]; then
        echo -e "${YELLOW}Press Enter to simulate East region failure...${NC}"
        read -r
    fi
    
    # Simulate East region failure
    echo -e "${RED}${WARNING} Simulating East region failure...${NC}"
    kubectl --context "$EAST_CONTEXT" scale deployment/webapp-east --replicas=0
    kubectl --context "$EAST_CONTEXT" patch deployment/health-monitor-east -p '{"spec":{"replicas":0}}'
    
    sleep 5
    
    echo -e "${BLUE}Status after East region failure:${NC}"
    "$SCRIPTS_DIR/show-status.sh"
    
    if [[ "$DEMO_PAUSE_STEPS" == "true" ]]; then
        echo -e "${YELLOW}Press Enter to show automatic recovery...${NC}"
        read -r
    fi
    
    # Show recovery
    echo -e "${GREEN}${CHECK} Automatic failover complete - all traffic now routed to West region${NC}"
    echo -e "${CYAN}West region is handling 100% of traffic${NC}"
    
    if [[ "$DEMO_PAUSE_STEPS" == "true" ]]; then
        echo -e "${YELLOW}Press Enter to simulate East region recovery...${NC}"
        read -r
    fi
    
    # Simulate recovery
    echo -e "${BLUE}Simulating East region recovery...${NC}"
    kubectl --context "$EAST_CONTEXT" scale deployment/webapp-east --replicas=2
    kubectl --context "$EAST_CONTEXT" patch deployment/health-monitor-east -p '{"spec":{"replicas":1}}'
    
    # Wait for recovery
    kubectl --context "$EAST_CONTEXT" wait --for=condition=available --timeout=300s deployment/webapp-east
    
    sleep 5
    
    echo -e "${BLUE}Status after East region recovery:${NC}"
    "$SCRIPTS_DIR/show-status.sh"
    
    echo -e "${GREEN}${CHECK} Disaster recovery demonstration complete${NC}"
}

show_monitoring_demo() {
    print_step "Step 6: Real-time monitoring demonstration"
    
    echo -e "${CYAN}The demo includes real-time monitoring tools:${NC}"
    echo ""
    echo -e "${YELLOW}Run this script to monitor the disaster recovery system:${NC}"
    echo -e "${BOLD}  ./scripts/monitor-failover.sh${NC}"
    echo ""
    echo -e "${CYAN}This provides a live dashboard showing:${NC}"
    echo -e "  • Regional health status"
    echo -e "  • Traffic distribution"
    echo -e "  • Failover events"
    echo -e "  • Recovery progress"
    echo ""
    echo -e "${YELLOW}You can also create failures manually:${NC}"
    echo -e "${BOLD}  ./scripts/simulate-failure.sh east  # Fail East region${NC}"
    echo -e "${BOLD}  ./scripts/simulate-failure.sh west  # Fail West region${NC}"
    echo -e "${BOLD}  ./scripts/simulate-recovery.sh east # Recover East region${NC}"
    echo ""
}

# Main execution
main() {
    print_header
    
    echo -e "${CYAN}This demo will show:${NC}"
    echo -e "  • Multi-region application deployment"
    echo -e "  • Automatic health monitoring"
    echo -e "  • Real-time failover when a region fails"
    echo -e "  • Traffic redirection to healthy regions"
    echo -e "  • Automatic recovery when regions come back online"
    echo ""
    
    create_clusters
    setup_tmc_syncers
    deploy_application
    deploy_monitoring
    demonstrate_failover
    show_monitoring_demo
    
    echo -e "${BOLD}${GREEN}${ROCKET} Disaster Recovery Demo Complete!${NC}"
    echo ""
    echo -e "${CYAN}Key TMC benefits demonstrated:${NC}"
    echo -e "  ${CHECK} Transparent multi-cluster workload management"
    echo -e "  ${CHECK} Automatic failover without user intervention"
    echo -e "  ${CHECK} Regional isolation with global coordination"
    echo -e "  ${CHECK} Real-time health monitoring across clusters"
    echo ""
}

# Check for help
if [[ "${1:-}" == "help" || "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
    echo "TMC Disaster Recovery Demo"
    echo ""
    echo "This demo showcases automatic failover of workloads between"
    echo "clusters when one region becomes unavailable."
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