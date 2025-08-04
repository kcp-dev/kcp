#!/bin/bash

# TMC Multi-Tenant Demo Cleanup Script
# Removes all demo resources and clusters

set -e

# Demo configuration
DEMO_DIR="$(dirname "$0")"
KUBECONFIG_DIR="$DEMO_DIR/kubeconfigs"
LOGS_DIR="$DEMO_DIR/logs"
REPORTS_DIR="$DEMO_DIR/reports"

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
    echo -e "${BOLD}${PURPLE}${CLEAN} TMC Multi-Tenant Demo Cleanup${NC}"
    echo -e "${CYAN}Removing all demo resources and clusters${NC}"
    echo ""
}

cleanup_kind_clusters() {
    echo -e "${BLUE}Cleaning up Kind clusters...${NC}"
    
    local clusters=("tenant-kcp" "tenant-shared" "tenant-isolated")
    
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

cleanup_reports() {
    echo -e "${BLUE}Cleaning up report files...${NC}"
    
    if [[ -d "$REPORTS_DIR" ]]; then
        local report_count=$(find "$REPORTS_DIR" -name "*.txt" 2>/dev/null | wc -l)
        if [[ $report_count -gt 0 ]]; then
            echo -e "${CYAN}Found $report_count report files${NC}"
            echo -e "${YELLOW}Keep report files? (y/N):${NC}"
            read -r keep_reports
            if [[ ! "$keep_reports" =~ ^[Yy]$ ]]; then
                rm -rf "$REPORTS_DIR"
                echo -e "${GREEN}${CHECK} Reports directory removed${NC}"
            else
                echo -e "${BLUE}Reports directory preserved${NC}"
            fi
        else
            rm -rf "$REPORTS_DIR"
            echo -e "${GREEN}${CHECK} Empty reports directory removed${NC}"
        fi
    else
        echo -e "${DIM}Reports directory not found${NC}"
    fi
}

cleanup_docker_containers() {
    echo -e "${BLUE}Cleaning up related Docker containers...${NC}"
    
    # Clean up any stale Kind containers
    local stale_containers=$(docker ps -a --filter "label=io.x-k8s.kind.cluster" --filter "name=tenant-" --format "{{.Names}}" 2>/dev/null || true)
    
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

cleanup_temporary_files() {
    echo -e "${BLUE}Cleaning up temporary files...${NC}"
    
    # Clean up any temporary tenant backups
    local temp_backups=$(find /tmp -name "tenant-backup-*" -type d 2>/dev/null || true)
    
    if [[ -n "$temp_backups" ]]; then
        echo -e "${CYAN}Found temporary tenant backups:${NC}"
        echo "$temp_backups"
        echo -e "${YELLOW}Remove temporary backups? (y/N):${NC}"
        read -r remove_backups
        if [[ "$remove_backups" =~ ^[Yy]$ ]]; then
            echo "$temp_backups" | while read -r backup_dir; do
                if [[ -n "$backup_dir" && -d "$backup_dir" ]]; then
                    rm -rf "$backup_dir"
                    echo -e "${DIM}Removed: $backup_dir${NC}"
                fi
            done
            echo -e "${GREEN}${CHECK} Temporary backups removed${NC}"
        else
            echo -e "${BLUE}Temporary backups preserved${NC}"
        fi
    else
        echo -e "${DIM}No temporary backups found${NC}"
    fi
}

verify_cleanup() {
    echo -e "${BLUE}Verifying cleanup completion...${NC}"
    
    # Check for remaining Kind clusters
    local remaining_clusters=$(kind get clusters 2>/dev/null | grep -E "^tenant-" || true)
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
    echo -e "${BOLD}${GREEN}${CHECK} Multi-Tenant Demo Cleanup Complete!${NC}"
    echo ""
    echo -e "${CYAN}Resources Cleaned Up:${NC}"
    echo -e "  • Kind clusters: tenant-kcp, tenant-shared, tenant-isolated"
    echo -e "  • Kubeconfig files and directories"
    echo -e "  • Demo log files"
    echo -e "  • Docker containers and unused networks"
    echo -e "  • Temporary files (if requested)"
    echo ""
    echo -e "${CYAN}What's Next:${NC}"
    echo -e "  • The demo can be run again with: ${BOLD}./run-demo.sh${NC}"
    echo -e "  • All tenant configurations are preserved in the manifests"
    echo -e "  • Scripts remain available for future use"
    echo ""
    echo -e "${DIM}Demo resources have been safely removed from your system.${NC}"
}

# Confirmation before cleanup
confirm_cleanup() {
    echo -e "${BOLD}${YELLOW}${WARNING} This will remove all multi-tenant demo resources!${NC}"
    echo ""
    echo -e "${CYAN}The following will be cleaned up:${NC}"
    echo -e "  • All Kind clusters (tenant-kcp, tenant-shared, tenant-isolated)"
    echo -e "  • Kubeconfig files and directories"
    echo -e "  • Demo log files"
    echo -e "  • Related Docker containers and networks"
    echo -e "  • Temporary files (with confirmation)"
    echo ""
    echo -e "${CYAN}The following will be preserved:${NC}"
    echo -e "  • Demo scripts and configurations"
    echo -e "  • Tenant manifest templates"
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
    cleanup_reports
    cleanup_docker_containers
    cleanup_docker_networks
    cleanup_temporary_files
    verify_cleanup
    show_cleanup_summary
}

# Check for help
if [[ "${1:-}" == "help" || "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
    echo "TMC Multi-Tenant Demo Cleanup Script"
    echo ""
    echo "Removes all demo resources and clusters created by the multi-tenant demo."
    echo ""
    echo "Usage: $0 [--force]"
    echo ""
    echo "Options:"
    echo "  --force    Skip confirmation prompt"
    echo ""
    echo "What gets cleaned up:"
    echo "  • Kind clusters (tenant-kcp, tenant-shared, tenant-isolated)"
    echo "  • Kubeconfig files and directories"
    echo "  • Demo log files"
    echo "  • Docker containers and unused networks"
    echo "  • Temporary files (with confirmation)"
    echo ""
    echo "What gets preserved:"
    echo "  • Demo scripts and configurations"
    echo "  • Tenant manifest templates"
    echo "  • Documentation files"
    echo "  • Report files (with confirmation)"
    echo ""
    exit 0
fi

# Run cleanup
main "$@"