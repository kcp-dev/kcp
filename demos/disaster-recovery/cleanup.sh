#!/bin/bash

# TMC Disaster Recovery Demo - Cleanup Script
# Removes all demo resources and kind clusters

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m' # No Color

# Unicode symbols
CHECK="✅"
CROSS="❌"
WARNING="⚠️"

# Demo directory
DEMO_DIR="$(dirname "$0")"
KUBECONFIG_DIR="$DEMO_DIR/kubeconfigs"
LOGS_DIR="$DEMO_DIR/logs"

print_header() {
    echo -e "${BOLD}${BLUE}================================================${NC}"
    echo -e "${BOLD}${BLUE}🧹 TMC Disaster Recovery Demo Cleanup${NC}"
    echo -e "${BOLD}${BLUE}================================================${NC}"
    echo ""
}

print_usage() {
    echo "TMC Disaster Recovery Demo - Cleanup Script"
    echo ""
    echo "Usage: $0 [options]"
    echo ""
    echo "Options:"
    echo "  --demo-only    Remove only demo resources, keep clusters"
    echo "  --full         Remove everything including clusters (default)"
    echo "  --force        Force cleanup, ignore errors"
    echo "  --help, -h     Show this help message"
    echo ""
    echo "This script will remove:"
    echo "  • Kind clusters (dr-kcp, dr-east, dr-west)"
    echo "  • Generated kubeconfig files"
    echo "  • Demo logs and temporary files"
    echo ""
}

cleanup_demo_resources() {
    echo -e "${BLUE}Cleaning up demo resources...${NC}"
    
    local contexts=("kind-dr-kcp" "kind-dr-east" "kind-dr-west")
    local force_flag="${1:-false}"
    
    for context in "${contexts[@]}"; do
        echo -e "${CYAN}Cleaning resources from $context...${NC}"
        
        if ! kubectl --context "$context" get nodes &>/dev/null; then
            echo -e "${YELLOW}${WARNING} Cluster $context not accessible, skipping...${NC}"
            continue
        fi
        
        # Remove deployments
        local deployments=$(kubectl --context "$context" get deployments --no-headers -o name 2>/dev/null | grep -E "(webapp|health-monitor|kcp-syncer|global-loadbalancer|failover-controller)" || true)
        if [[ -n "$deployments" ]]; then
            echo -e "${CYAN}  Removing deployments...${NC}"
            echo "$deployments" | xargs -r kubectl --context "$context" delete --ignore-not-found=true &>/dev/null || [[ "$force_flag" == "true" ]]
        fi
        
        # Remove services
        local services=$(kubectl --context "$context" get services --no-headers -o name 2>/dev/null | grep -E "(webapp|health-monitor|global-loadbalancer)" || true)
        if [[ -n "$services" ]]; then
            echo -e "${CYAN}  Removing services...${NC}"
            echo "$services" | xargs -r kubectl --context "$context" delete --ignore-not-found=true &>/dev/null || [[ "$force_flag" == "true" ]]
        fi
        
        # Remove configmaps
        local configmaps=$(kubectl --context "$context" get configmaps --no-headers -o name 2>/dev/null | grep -E "(webapp-content)" || true)
        if [[ -n "$configmaps" ]]; then
            echo -e "${CYAN}  Removing configmaps...${NC}"
            echo "$configmaps" | xargs -r kubectl --context "$context" delete --ignore-not-found=true &>/dev/null || [[ "$force_flag" == "true" ]]
        fi
        
        # Remove RBAC resources
        echo -e "${CYAN}  Removing RBAC resources...${NC}"
        kubectl --context "$context" delete clusterrolebinding --ignore-not-found=true \
            global-loadbalancer health-monitor kcp-syncer failover-controller &>/dev/null || [[ "$force_flag" == "true" ]]
        kubectl --context "$context" delete clusterrole --ignore-not-found=true \
            global-loadbalancer health-monitor kcp-syncer failover-controller &>/dev/null || [[ "$force_flag" == "true" ]]
        kubectl --context "$context" delete serviceaccount --ignore-not-found=true \
            global-loadbalancer health-monitor kcp-syncer failover-controller &>/dev/null || [[ "$force_flag" == "true" ]]
        
        echo -e "${GREEN}${CHECK} $context resources cleaned${NC}"
    done
    
    echo -e "${GREEN}${CHECK} Demo resources cleanup completed${NC}"
}

cleanup_clusters() {
    echo -e "${BLUE}Removing kind clusters...${NC}"
    
    local clusters=("dr-kcp" "dr-east" "dr-west")
    local force_flag="${1:-false}"
    
    for cluster in "${clusters[@]}"; do
        echo -e "${CYAN}Removing cluster: $cluster${NC}"
        if kind delete cluster --name "$cluster" &>/dev/null || [[ "$force_flag" == "true" ]]; then
            echo -e "${GREEN}${CHECK} Cluster $cluster removed${NC}"
        else
            if [[ "$force_flag" == "true" ]]; then
                echo -e "${YELLOW}${WARNING} Failed to remove cluster $cluster (ignored due to --force)${NC}"
            else
                echo -e "${RED}${CROSS} Failed to remove cluster $cluster${NC}"
            fi
        fi
    done
    
    echo -e "${GREEN}${CHECK} Clusters cleanup completed${NC}"
}

cleanup_files() {
    echo -e "${BLUE}Cleaning up generated files...${NC}"
    
    # Remove kubeconfig directory
    if [[ -d "$KUBECONFIG_DIR" ]]; then
        rm -rf "$KUBECONFIG_DIR"
        echo -e "${GREEN}${CHECK} Removed kubeconfig directory${NC}"
    fi
    
    # Remove logs directory
    if [[ -d "$LOGS_DIR" ]]; then
        rm -rf "$LOGS_DIR"
        echo -e "${GREEN}${CHECK} Removed logs directory${NC}"
    fi
    
    # Remove any temporary files
    find "$DEMO_DIR" -name "*.tmp" -delete 2>/dev/null || true
    find "$DEMO_DIR" -name "*.log" -delete 2>/dev/null || true
    
    echo -e "${GREEN}${CHECK} File cleanup completed${NC}"
}

# Main execution
main() {
    local demo_only=false
    local force_cleanup=false
    
    # Parse arguments
    while [[ $# -gt 0 ]]; do
        case $1 in
            --demo-only)
                demo_only=true
                shift
                ;;
            --full)
                demo_only=false
                shift
                ;;
            --force)
                force_cleanup=true
                shift
                ;;
            --help|-h)
                print_usage
                exit 0
                ;;
            *)
                echo -e "${RED}Unknown option: $1${NC}"
                print_usage
                exit 1
                ;;
        esac
    done
    
    print_header
    
    if [[ "$demo_only" == "true" ]]; then
        echo -e "${CYAN}Performing demo-only cleanup (keeping clusters)${NC}"
        echo ""
        cleanup_demo_resources "$force_cleanup"
        cleanup_files
    else
        echo -e "${CYAN}Performing full cleanup (removing everything)${NC}"
        echo ""
        
        # Ask for confirmation unless force is specified
        if [[ "$force_cleanup" != "true" ]]; then
            echo -e "${YELLOW}This will remove all demo resources and kind clusters.${NC}"
            echo -e "${YELLOW}Are you sure you want to continue? (y/N)${NC}"
            read -r response
            case "$response" in
                [yY][eE][sS]|[yY])
                    echo -e "${CYAN}Proceeding with cleanup...${NC}"
                    ;;
                *)
                    echo -e "${YELLOW}Cleanup cancelled${NC}"
                    exit 0
                    ;;
            esac
        fi
        
        cleanup_demo_resources "$force_cleanup"
        cleanup_clusters "$force_cleanup"
        cleanup_files
    fi
    
    echo ""
    echo -e "${BOLD}${GREEN}🎉 Cleanup completed successfully!${NC}"
    echo ""
    echo -e "${CYAN}To run the demo again:${NC}"
    echo -e "${YELLOW}  ./run-demo.sh${NC}"
    echo ""
}

# Run the script
main "$@"