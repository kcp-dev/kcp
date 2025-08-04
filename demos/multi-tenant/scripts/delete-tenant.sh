#!/bin/bash

# TMC Delete Tenant Script
# Safely removes a tenant and all associated resources

set -e

# Check if tenant name is provided
if [[ $# -eq 0 ]]; then
    echo "Usage: $0 <tenant-name> [--force]"
    echo "Example: $0 old-company"
    echo "Use --force to skip confirmation"
    exit 1
fi

TENANT_NAME="$1"
FORCE_DELETE="${2:-}"

# Demo configuration
DEMO_DIR="$(dirname "$0")/.."
KUBECONFIG_DIR="$DEMO_DIR/kubeconfigs"

# Cluster contexts
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
WARNING="⚠️"
TENANT="👥"

print_header() {
    echo -e "${BOLD}${RED}${TENANT} Deleting Tenant: $TENANT_NAME${NC}"
    echo ""
}

find_tenant_cluster() {
    # Check shared cluster first
    if kubectl --context "$SHARED_CONTEXT" get namespace "tenant-$TENANT_NAME" &>/dev/null; then
        echo "shared"
        return 0
    fi
    
    # Check isolated cluster
    if kubectl --context "$ISOLATED_CONTEXT" get namespace "tenant-$TENANT_NAME" &>/dev/null; then
        echo "isolated"
        return 0
    fi
    
    echo ""
    return 1
}

show_tenant_resources() {
    local context="$1"
    
    echo -e "${CYAN}Tenant resources to be deleted:${NC}"
    echo ""
    
    # Show namespace resources
    echo -e "${BLUE}Namespace: tenant-$TENANT_NAME${NC}"
    kubectl --context "$context" get all -n "tenant-$TENANT_NAME" 2>/dev/null || echo "  No resources found"
    echo ""
    
    # Show resource quotas
    echo -e "${BLUE}Resource Quotas:${NC}"
    kubectl --context "$context" get resourcequota -n "tenant-$TENANT_NAME" 2>/dev/null || echo "  No quotas found"
    echo ""
    
    # Show network policies
    echo -e "${BLUE}Network Policies:${NC}"
    kubectl --context "$context" get networkpolicy -n "tenant-$TENANT_NAME" 2>/dev/null || echo "  No policies found"
    echo ""
    
    # Show RBAC
    echo -e "${BLUE}RBAC Resources:${NC}"
    kubectl --context "$context" get role,rolebinding -n "tenant-$TENANT_NAME" 2>/dev/null || echo "  No RBAC found"
    echo ""
    
    # Show persistent volumes
    echo -e "${BLUE}Persistent Volume Claims:${NC}"
    kubectl --context "$context" get pvc -n "tenant-$TENANT_NAME" 2>/dev/null || echo "  No PVCs found"
    echo ""
}

confirm_deletion() {
    if [[ "$FORCE_DELETE" == "--force" ]]; then
        echo -e "${YELLOW}${WARNING} Force deletion requested, skipping confirmation${NC}"
        return 0
    fi
    
    echo -e "${BOLD}${RED}${WARNING} WARNING: This will permanently delete the tenant and ALL associated resources!${NC}"
    echo ""
    echo -e "${YELLOW}This action cannot be undone. All data, configurations, and applications"
    echo -e "associated with tenant '$TENANT_NAME' will be permanently removed.${NC}"
    echo ""
    echo -e "${CYAN}Type 'DELETE' to confirm, or press Enter to cancel:${NC}"
    read -r confirmation
    
    if [[ "$confirmation" != "DELETE" ]]; then
        echo -e "${GREEN}Deletion cancelled${NC}"
        exit 0
    fi
    
    echo -e "${YELLOW}Proceeding with tenant deletion...${NC}"
    echo ""
}

backup_tenant_data() {
    local context="$1"
    local backup_dir="/tmp/tenant-backup-$TENANT_NAME-$(date +%Y%m%d-%H%M%S)"
    
    echo -e "${BLUE}Creating backup of tenant configuration...${NC}"
    
    mkdir -p "$backup_dir"
    
    # Backup namespace configuration
    kubectl --context "$context" get namespace "tenant-$TENANT_NAME" -o yaml > "$backup_dir/namespace.yaml" 2>/dev/null || true
    
    # Backup all resources
    kubectl --context "$context" get all -n "tenant-$TENANT_NAME" -o yaml > "$backup_dir/resources.yaml" 2>/dev/null || true
    
    # Backup RBAC
    kubectl --context "$context" get role,rolebinding -n "tenant-$TENANT_NAME" -o yaml > "$backup_dir/rbac.yaml" 2>/dev/null || true
    
    # Backup network policies
    kubectl --context "$context" get networkpolicy -n "tenant-$TENANT_NAME" -o yaml > "$backup_dir/network-policies.yaml" 2>/dev/null || true
    
    # Backup resource quotas
    kubectl --context "$context" get resourcequota,limitrange -n "tenant-$TENANT_NAME" -o yaml > "$backup_dir/quotas.yaml" 2>/dev/null || true
    
    echo -e "${GREEN}${CHECK} Backup created at: $backup_dir${NC}"
}

delete_tenant_resources() {
    local context="$1"
    
    echo -e "${BLUE}Deleting tenant resources...${NC}"
    
    # Delete applications first (graceful shutdown)
    echo -e "${CYAN}Scaling down applications...${NC}"
    kubectl --context "$context" scale deployment --all --replicas=0 -n "tenant-$TENANT_NAME" 2>/dev/null || true
    sleep 5
    
    # Delete the entire namespace (this removes everything)
    echo -e "${CYAN}Deleting tenant namespace...${NC}"
    kubectl --context "$context" delete namespace "tenant-$TENANT_NAME" --ignore-not-found=true
    
    # Wait for namespace to be fully deleted
    echo -e "${CYAN}Waiting for namespace deletion to complete...${NC}"
    local timeout=60
    while kubectl --context "$context" get namespace "tenant-$TENANT_NAME" &>/dev/null && [[ $timeout -gt 0 ]]; do
        echo -e "${DIM}Waiting for namespace deletion... (${timeout}s remaining)${NC}"
        sleep 5
        timeout=$((timeout - 5))
    done
    
    if [[ $timeout -le 0 ]]; then
        echo -e "${YELLOW}${WARNING} Namespace deletion is taking longer than expected${NC}"
        echo -e "${CYAN}This is normal for namespaces with persistent volumes or finalizers${NC}"
    else
        echo -e "${GREEN}${CHECK} Namespace deleted successfully${NC}"
    fi
}

verify_deletion() {
    local context="$1"
    
    echo -e "${BLUE}Verifying tenant deletion...${NC}"
    
    # Check if namespace is gone
    if ! kubectl --context "$context" get namespace "tenant-$TENANT_NAME" &>/dev/null; then
        echo -e "${GREEN}${CHECK} Tenant namespace removed${NC}"
    else
        echo -e "${YELLOW}${WARNING} Tenant namespace still exists (may be in terminating state)${NC}"
    fi
    
    # Check for any remaining resources
    local remaining_resources=$(kubectl --context "$context" get all -n "tenant-$TENANT_NAME" 2>/dev/null | wc -l || echo "0")
    if [[ $remaining_resources -eq 0 ]]; then
        echo -e "${GREEN}${CHECK} All tenant resources removed${NC}"
    else
        echo -e "${YELLOW}${WARNING} Some resources may still be terminating${NC}"
    fi
}

show_deletion_summary() {
    local cluster_tier="$1"
    
    echo ""
    echo -e "${BOLD}${GREEN}${CHECK} Tenant '$TENANT_NAME' deletion completed!${NC}"
    echo ""
    echo -e "${CYAN}Deletion Summary:${NC}"
    echo -e "  • Tenant Name: $TENANT_NAME"
    echo -e "  • Cluster Tier: $cluster_tier"
    echo -e "  • Namespace: tenant-$TENANT_NAME (deleted)"
    echo -e "  • Resources: All tenant resources removed"
    echo -e "  • Backup: Configuration backed up to /tmp/"
    echo ""
    echo -e "${CYAN}Remaining Management Commands:${NC}"
    echo -e "  ${BOLD}./create-tenant.sh <name> [tier]${NC}       Create new tenant"
    echo -e "  ${BOLD}./monitor-tenants.sh${NC}                   Monitor remaining tenants"
    echo -e "  ${BOLD}./tenant-resource-report.sh${NC}            Generate usage report"
    echo ""
}

# Main execution
main() {
    print_header
    
    # Find which cluster the tenant is in
    echo -e "${BLUE}Locating tenant...${NC}"
    local cluster_tier
    cluster_tier=$(find_tenant_cluster)
    
    if [[ -z "$cluster_tier" ]]; then
        echo -e "${RED}${CROSS} Tenant '$TENANT_NAME' not found in any cluster${NC}"
        exit 1
    fi
    
    local context="$SHARED_CONTEXT"
    if [[ "$cluster_tier" == "isolated" ]]; then
        context="$ISOLATED_CONTEXT"
    fi
    
    echo -e "${GREEN}${CHECK} Found tenant '$TENANT_NAME' in $cluster_tier cluster${NC}"
    echo ""
    
    # Show what will be deleted
    show_tenant_resources "$context"
    
    # Confirm deletion
    confirm_deletion
    
    # Create backup
    backup_tenant_data "$context"
    
    # Delete tenant
    delete_tenant_resources "$context"
    
    # Verify deletion
    verify_deletion "$context"
    
    # Show summary
    show_deletion_summary "$cluster_tier"
}

# Check for help
if [[ "${1:-}" == "help" || "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
    echo "TMC Delete Tenant Script"
    echo ""
    echo "Safely removes a tenant and all associated resources."
    echo ""
    echo "Usage: $0 <tenant-name> [--force]"
    echo ""
    echo "Parameters:"
    echo "  tenant-name    Name of the tenant to delete"
    echo "  --force       Skip confirmation prompt"
    echo ""
    echo "Examples:"
    echo "  $0 old-company              Delete tenant with confirmation"
    echo "  $0 test-tenant --force      Delete tenant without confirmation"
    echo ""
    echo "Safety Features:"
    echo "  • Creates backup before deletion"
    echo "  • Requires explicit confirmation (unless --force)"
    echo "  • Gracefully scales down applications first"
    echo "  • Verifies deletion completion"
    echo ""
    exit 0
fi

# Run the script
main "$@"