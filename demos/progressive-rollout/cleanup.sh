#!/bin/bash

# TMC Progressive Rollout Demo Cleanup Script
# Removes all demo resources and clusters

set -e

# Demo configuration
DEMO_DIR="$(dirname "$0")"
KUBECONFIG_DIR="$DEMO_DIR/kubeconfigs"
LOGS_DIR="$DEMO_DIR/logs"

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
WARNING="⚠️"
CLEAN="🧹"

print_header() {
    echo -e "${BOLD}${PURPLE}${CLEAN} TMC Progressive Rollout Demo Cleanup${NC}"
    echo -e "${CYAN}Removing all demo resources and clusters${NC}"
    echo ""
}

cleanup_kind_clusters() {
    echo -e "${BLUE}Cleaning up Kind clusters...${NC}"
    
    local clusters=("rollout-kcp" "rollout-canary" "rollout-staging" "rollout-prod")
    
    for cluster in "${clusters[@]}"; do
        if kind get clusters 2>/dev/null | grep -q "^${cluster}$"; then
            echo -e "${CYAN}Deleting cluster: $cluster${NC}"
            kind delete cluster --name "$cluster" || echo -e "${YELLOW}${WARNING} Failed to delete $cluster${NC}"
        else
            echo -e "${DIM}Cluster $cluster not found${NC}"
        fi
    done
    
    echo -e "${GREEN}${CHECK} Kind clusters cleanup completed${NC}"
}

cleanup_kubeconfig_files() {
    echo -e "${BLUE}Cleaning up kubeconfig files...${NC}"
    
    if [[ -d "$KUBECONFIG_DIR" ]]; then
        rm -rf "$KUBECONFIG_DIR"
        echo -e "${GREEN}${CHECK} Kubeconfig directory removed${NC}"
    else
        echo -e "${DIM}Kubeconfig directory not found${NC}"
    fi
}

cleanup_logs() {
    echo -e "${BLUE}Cleaning up log files...${NC}"
    
    if [[ -d "$LOGS_DIR" ]]; then
        rm -rf "$LOGS_DIR"
        echo -e "${GREEN}${CHECK} Logs directory removed${NC}"
    else
        echo -e "${DIM}Logs directory not found${NC}"
    fi
}

cleanup_docker_containers() {
    echo -e "${BLUE}Cleaning up related Docker containers...${NC}"
    
    # Clean up any stale Kind containers
    local stale_containers=$(docker ps -a --filter "label=io.x-k8s.kind.cluster" --filter "name=rollout-" --format "{{.Names}}" 2>/dev/null || true)
    
    if [[ -n "$stale_containers" ]]; then
        echo -e "${CYAN}Removing stale Kind containers...${NC}"
        echo "$stale_containers" | while read -r container; do
            echo -e "${DIM}Removing container: $container${NC}"
            docker rm -f "$container" &>/dev/null || true
        done
        echo -e "${GREEN}${CHECK} Stale containers removed${NC}"
    else
        echo -e "${DIM}No stale containers found${NC}"
    fi
}

cleanup_docker_networks() {
    echo -e "${BLUE}Cleaning up Docker networks...${NC}"
    
    # Clean up Kind networks
    local kind_networks=$(docker network ls --filter "name=kind" --format "{{.Name}}" 2>/dev/null || true)
    
    if [[ -n "$kind_networks" ]]; then
        echo -e "${CYAN}Found Kind networks, checking if safe to remove...${NC}"
        for network in $kind_networks; do
            # Only remove if no containers are using it
            local network_containers=$(docker network inspect "$network" --format "{{len .Containers}}" 2>/dev/null || echo "0")
            if [[ "$network_containers" == "0" ]]; then
                echo -e "${DIM}Removing unused network: $network${NC}"
                docker network rm "$network" &>/dev/null || true
            fi
        done
        echo -e "${GREEN}${CHECK} Unused networks cleaned up${NC}"
    else
        echo -e "${DIM}No Kind networks found${NC}"
    fi
}

verify_cleanup() {
    echo -e "${BLUE}Verifying cleanup completion...${NC}"
    
    # Check for remaining Kind clusters
    local remaining_clusters=$(kind get clusters 2>/dev/null | grep -E "^rollout-" || true)
    if [[ -n "$remaining_clusters" ]]; then
        echo -e "${YELLOW}${WARNING} Some clusters may still exist:${NC}"
        echo "$remaining_clusters"
    else
        echo -e "${GREEN}${CHECK} All demo clusters removed${NC}"
    fi
    
    # Check for remaining directories
    local remaining_dirs=()
    [[ -d "$KUBECONFIG_DIR" ]] && remaining_dirs+=("kubeconfigs")
    [[ -d "$LOGS_DIR" ]] && remaining_dirs+=("logs")
    
    if [[ ${#remaining_dirs[@]} -gt 0 ]]; then
        echo -e "${YELLOW}${WARNING} Some directories still exist: ${remaining_dirs[*]}${NC}"
    else
        echo -e "${GREEN}${CHECK} All temporary directories removed${NC}"
    fi
    
    echo -e "${GREEN}${CHECK} Cleanup verification completed${NC}"
}

show_cleanup_summary() {
    echo ""
    echo -e "${BOLD}${GREEN}${CHECK} Progressive Rollout Demo Cleanup Complete!${NC}"
    echo ""
    echo -e "${CYAN}Resources Cleaned Up:${NC}"
    echo -e "  • Kind clusters: rollout-kcp, rollout-canary, rollout-staging, rollout-prod"
    echo -e "  • Kubeconfig files and directories"
    echo -e "  • Demo log files"
    echo -e "  • Docker containers and unused networks"
    echo ""
    echo -e "${CYAN}What's Next:${NC}"
    echo -e "  • The demo can be run again with: ${BOLD}./run-demo.sh${NC}"
    echo -e "  • All rollout configurations are preserved in the manifests"
    echo -e "  • Scripts remain available for future use"
    echo ""
    echo -e "${DIM}Demo resources have been safely removed from your system.${NC}"
}

# Confirmation before cleanup
confirm_cleanup() {
    echo -e "${BOLD}${YELLOW}${WARNING} This will remove all progressive rollout demo resources!${NC}"
    echo ""
    echo -e "${CYAN}The following will be cleaned up:${NC}"
    echo -e "  • All Kind clusters (rollout-kcp, rollout-canary, rollout-staging, rollout-prod)"
    echo -e "  • Kubeconfig files and directories"
    echo -e "  • Demo log files"
    echo -e "  • Related Docker containers and networks"
    echo ""
    echo -e "${CYAN}The following will be preserved:${NC}"
    echo -e "  • Demo scripts and configurations"
    echo -e "  • Application manifest templates"
    echo -e "  • Documentation files"
    echo ""
    echo -e "${YELLOW}Continue with cleanup? (y/N):${NC}"
    read -r confirm
    
    if [[ ! "$confirm" =~ ^[Yy]$ ]]; then
        echo -e "${BLUE}Cleanup cancelled${NC}"
        exit 0
    fi
}

# Main execution
main() {
    print_header
    
    # Skip confirmation if --force flag is provided
    if [[ "${1:-}" != "--force" ]]; then
        confirm_cleanup
    fi
    
    echo -e "${CYAN}Starting cleanup process...${NC}"
    echo ""
    
    cleanup_kind_clusters
    cleanup_kubeconfig_files
    cleanup_logs
    cleanup_docker_containers
    cleanup_docker_networks
    verify_cleanup
    show_cleanup_summary
}

# Check for help
if [[ "${1:-}" == "help" || "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
    echo "TMC Progressive Rollout Demo Cleanup Script"
    echo ""
    echo "Removes all demo resources and clusters created by the progressive rollout demo."
    echo ""
    echo "Usage: $0 [--force]"
    echo ""
    echo "Options:"
    echo "  --force    Skip confirmation prompt"
    echo ""
    echo "What gets cleaned up:"
    echo "  • Kind clusters (rollout-kcp, rollout-canary, rollout-staging, rollout-prod)"
    echo "  • Kubeconfig files and directories"
    echo "  • Demo log files"
    echo "  • Docker containers and unused networks"
    echo ""
    echo "What gets preserved:"
    echo "  • Demo scripts and configurations"
    echo "  • Application manifest templates"
    echo "  • Documentation files"
    echo ""
    exit 0
fi

# Run cleanup
main "$@"