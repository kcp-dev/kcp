#!/bin/bash

# TMC Cross-Cluster Controller - Demo TaskQueue Creator
# Creates sample TaskQueues on different clusters to demonstrate cross-cluster processing

set -e

# Demo configuration
DEMO_DIR="$(dirname "$0")/.."
MANIFESTS_DIR="$DEMO_DIR/manifests"

# Cluster contexts
KCP_CONTEXT="kind-controller-kcp"
EAST_CONTEXT="kind-controller-east"
WEST_CONTEXT="kind-controller-west"

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

print_header() {
    echo -e "${BOLD}${PURPLE}================================================${NC}"
    echo -e "${BOLD}${PURPLE}${ROCKET} TMC Demo TaskQueue Creator${NC}"
    echo -e "${BOLD}${PURPLE}================================================${NC}"
    echo -e "${CYAN}Creating sample TaskQueues to demonstrate cross-cluster processing${NC}"
    echo ""
}

# Check if demo clusters are running
check_clusters() {
    echo -e "${BLUE}Checking cluster availability...${NC}"
    
    local clusters_ready=0
    
    if kubectl --context "$KCP_CONTEXT" get nodes &>/dev/null; then
        echo -e "${GREEN}${CHECK} KCP cluster: Ready${NC}"
        clusters_ready=$((clusters_ready + 1))
    else
        echo -e "${RED}${CROSS} KCP cluster: Not available${NC}"
    fi
    
    if kubectl --context "$EAST_CONTEXT" get nodes &>/dev/null; then
        echo -e "${GREEN}${CHECK} East cluster: Ready${NC}"
        clusters_ready=$((clusters_ready + 1))
    else
        echo -e "${RED}${CROSS} East cluster: Not available${NC}"
    fi
    
    if kubectl --context "$WEST_CONTEXT" get nodes &>/dev/null; then
        echo -e "${GREEN}${CHECK} West cluster: Ready${NC}"
        clusters_ready=$((clusters_ready + 1))
    else
        echo -e "${RED}${CROSS} West cluster: Not available${NC}"
    fi
    
    if [[ $clusters_ready -lt 3 ]]; then
        echo -e "${RED}${CROSS} Not all clusters are available. Please run the demo first:${NC}"
        echo -e "${YELLOW}  cd /mnt/scratch/workspace/kcp/demos/cross-cluster-controller${NC}"
        echo -e "${YELLOW}  ./run-demo.sh${NC}"
        exit 1
    fi
    
    echo ""
}

# Create TaskQueue on a specific cluster
create_taskqueue() {
    local context=$1
    local cluster_name=$2
    local manifest_file=$3
    local taskqueue_name=$4
    
    echo -e "${BLUE}Creating TaskQueue '$taskqueue_name' on $cluster_name cluster...${NC}"
    
    if kubectl --context "$context" apply -f "$manifest_file"; then
        echo -e "${GREEN}${CHECK} TaskQueue '$taskqueue_name' created on $cluster_name${NC}"
        
        # Wait a moment for the resource to be created
        sleep 2
        
        # Show the created TaskQueue
        echo -e "${CYAN}TaskQueue details:${NC}"
        kubectl --context "$context" get taskqueue "$taskqueue_name" -o wide || true
        echo ""
    else
        echo -e "${RED}${CROSS} Failed to create TaskQueue '$taskqueue_name' on $cluster_name${NC}"
        return 1
    fi
}

# Create TaskQueues on different clusters
create_demo_taskqueues() {
    echo -e "${BOLD}${CLUSTER} Creating TaskQueues across clusters...${NC}"
    echo ""
    
    # Create TaskQueue on East cluster
    if [[ -f "$MANIFESTS_DIR/east-taskqueue.yaml" ]]; then
        create_taskqueue "$EAST_CONTEXT" "East" "$MANIFESTS_DIR/east-taskqueue.yaml" "east-data-processing"
    else
        echo -e "${YELLOW}⚠️  East TaskQueue manifest not found, skipping...${NC}"
    fi
    
    # Create TaskQueue on West cluster  
    if [[ -f "$MANIFESTS_DIR/west-taskqueue.yaml" ]]; then
        create_taskqueue "$WEST_CONTEXT" "West" "$MANIFESTS_DIR/west-taskqueue.yaml" "west-ml-training"
    else
        echo -e "${YELLOW}⚠️  West TaskQueue manifest not found, skipping...${NC}"
    fi
    
    # Create TaskQueue on KCP (global)
    if [[ -f "$MANIFESTS_DIR/global-taskqueue.yaml" ]]; then
        create_taskqueue "$KCP_CONTEXT" "KCP" "$MANIFESTS_DIR/global-taskqueue.yaml" "global-health-monitor"
    else
        echo -e "${YELLOW}⚠️  Global TaskQueue manifest not found, skipping...${NC}"
    fi
}

# Show cross-cluster visibility
show_cross_cluster_status() {
    echo -e "${BOLD}${CLUSTER} Verifying Cross-Cluster Visibility...${NC}"
    echo ""
    
    local clusters=("$KCP_CONTEXT:KCP" "$EAST_CONTEXT:East" "$WEST_CONTEXT:West")
    
    for cluster_info in "${clusters[@]}"; do
        IFS=':' read -r context cluster_name <<< "$cluster_info"
        
        echo -e "${CYAN}TaskQueues visible from $cluster_name cluster:${NC}"
        kubectl --context "$context" get taskqueues -o wide || echo "  No TaskQueues found"
        echo ""
    done
}

# Show monitoring script info
show_monitoring_info() {
    echo -e "${BOLD}${ROCKET} Next Steps${NC}"
    echo ""
    echo -e "${GREEN}${CHECK} TaskQueues created successfully!${NC}"
    echo ""
    echo -e "${CYAN}To monitor cross-cluster processing in real-time:${NC}"
    echo -e "${YELLOW}  ./scripts/monitor-processing.sh${NC}"
    echo ""
    echo -e "${CYAN}The monitoring script will show:${NC}"
    echo -e "  • Real-time TaskQueue status across all clusters"
    echo -e "  • Which cluster is processing each TaskQueue"
    echo -e "  • Cross-cluster resource synchronization"
    echo -e "  • Controller activity and health"
    echo ""
    echo -e "${CYAN}Key insights to observe:${NC}"
    echo -e "  • ${YELLOW}east-data-processing${NC} created on East, processed by West controller"
    echo -e "  • ${YELLOW}west-ml-training${NC} created on West, processed by West controller"
    echo -e "  • ${YELLOW}global-health-monitor${NC} created on KCP, processed by West controller"
    echo ""
}

# Cleanup function
cleanup_taskqueues() {
    echo -e "${YELLOW}Cleaning up TaskQueues...${NC}"
    
    local taskqueues=("east-data-processing" "west-ml-training" "global-health-monitor")
    local clusters=("$KCP_CONTEXT" "$EAST_CONTEXT" "$WEST_CONTEXT")
    
    for taskqueue in "${taskqueues[@]}"; do
        for context in "${clusters[@]}"; do
            kubectl --context "$context" delete taskqueue "$taskqueue" --ignore-not-found=true &>/dev/null || true
        done
    done
    
    echo -e "${GREEN}${CHECK} TaskQueues cleaned up${NC}"
}

# Main function
main() {
    case "${1:-create}" in
        "create"|"")
            print_header
            check_clusters
            create_demo_taskqueues
            show_cross_cluster_status
            show_monitoring_info
            ;;
        "cleanup"|"clean")
            echo -e "${YELLOW}Cleaning up demo TaskQueues...${NC}"
            cleanup_taskqueues
            ;;
        "status"|"show")
            echo -e "${CYAN}Current TaskQueue status across clusters:${NC}"
            echo ""
            show_cross_cluster_status
            ;;
        "help"|"-h"|"--help")
            echo "TMC Demo TaskQueue Creator"
            echo ""
            echo "Usage: $0 [command]"
            echo ""
            echo "Commands:"
            echo "  create     Create demo TaskQueues (default)"
            echo "  cleanup    Remove all demo TaskQueues"
            echo "  status     Show current TaskQueue status"
            echo "  help       Show this help message"
            echo ""
            echo "This script creates sample TaskQueues on different clusters"
            echo "to demonstrate cross-cluster controller processing."
            echo ""
            ;;
        *)
            echo -e "${RED}Unknown command: $1${NC}"
            echo -e "${YELLOW}Use '$0 help' for usage information${NC}"
            exit 1
            ;;
    esac
}

# Run the script
main "$@"