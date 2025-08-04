#!/bin/bash

# TMC Create Tenant Script
# Creates a new tenant workspace with proper isolation and resources

set -e

# Check if tenant name is provided
if [[ $# -eq 0 ]]; then
    echo "Usage: $0 <tenant-name> [shared|isolated]"
    echo "Example: $0 new-company shared"
    exit 1
fi

TENANT_NAME="$1"
TENANT_TIER="${2:-shared}"

# Demo configuration
DEMO_DIR="$(dirname "$0")/.."
TENANTS_DIR="$DEMO_DIR/tenants"
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
TENANT="👥"
ISOLATION="🔒"

print_header() {
    echo -e "${BOLD}${PURPLE}${TENANT} Creating New Tenant: $TENANT_NAME${NC}"
    echo -e "${CYAN}Tenant Tier: $TENANT_TIER${NC}"
    echo ""
}

validate_tenant_name() {
    # Validate tenant name format
    if [[ ! "$TENANT_NAME" =~ ^[a-z0-9][a-z0-9-]*[a-z0-9]$ ]]; then
        echo -e "${RED}${CROSS} Invalid tenant name. Use lowercase letters, numbers, and hyphens only.${NC}"
        exit 1
    fi
    
    # Check if tenant already exists
    local context="$SHARED_CONTEXT"
    if [[ "$TENANT_TIER" == "isolated" ]]; then
        context="$ISOLATED_CONTEXT"
    fi
    
    if kubectl --context "$context" get namespace "tenant-$TENANT_NAME" &>/dev/null; then
        echo -e "${RED}${CROSS} Tenant '$TENANT_NAME' already exists in $TENANT_TIER tier${NC}"
        exit 1
    fi
    
    echo -e "${GREEN}${CHECK} Tenant name validation passed${NC}"
}

create_tenant_namespace() {
    local context="$SHARED_CONTEXT"
    if [[ "$TENANT_TIER" == "isolated" ]]; then
        context="$ISOLATED_CONTEXT"
    fi
    
    echo -e "${BLUE}Creating tenant namespace...${NC}"
    
    kubectl --context "$context" create namespace "tenant-$TENANT_NAME" --dry-run=client -o yaml | \
    kubectl --context "$context" apply -f -
    
    # Label the namespace
    kubectl --context "$context" label namespace "tenant-$TENANT_NAME" \
        tenant="$TENANT_NAME" \
        tier="$TENANT_TIER" \
        demo="multi-tenant" \
        created-by="create-tenant-script" \
        created-at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    
    echo -e "${GREEN}${CHECK} Tenant namespace created and labeled${NC}"
}

create_tenant_resources() {
    local context="$SHARED_CONTEXT"
    local template_file="$TENANTS_DIR/tenant-template.yaml"
    
    if [[ "$TENANT_TIER" == "isolated" ]]; then
        context="$ISOLATED_CONTEXT"
        template_file="$TENANTS_DIR/isolated-tenant-template.yaml"
    fi
    
    echo -e "${BLUE}Creating tenant resources (RBAC, quotas, policies)...${NC}"
    
    # Apply tenant-specific manifests
    sed "s/TENANT_NAME/$TENANT_NAME/g" "$template_file" | \
    kubectl --context "$context" apply -f -
    
    echo -e "${GREEN}${CHECK} Tenant resources created${NC}"
}

deploy_tenant_application() {
    local context="$SHARED_CONTEXT"
    local app_template="$TENANTS_DIR/tenant-app-template.yaml"
    
    if [[ "$TENANT_TIER" == "isolated" ]]; then
        context="$ISOLATED_CONTEXT"
        app_template="$TENANTS_DIR/isolated-tenant-app.yaml"
    fi
    
    echo -e "${BLUE}Deploying tenant application...${NC}"
    
    # Deploy tenant application
    sed "s/TENANT_NAME/$TENANT_NAME/g" "$app_template" | \
    kubectl --context "$context" apply -f -
    
    echo -e "${GREEN}${CHECK} Tenant application deployed${NC}"
}

verify_tenant_creation() {
    local context="$SHARED_CONTEXT"
    if [[ "$TENANT_TIER" == "isolated" ]]; then
        context="$ISOLATED_CONTEXT"
    fi
    
    echo -e "${BLUE}Verifying tenant creation...${NC}"
    
    # Wait for resources to be ready
    echo -e "${CYAN}Waiting for tenant resources to be ready...${NC}"
    sleep 10
    
    # Check namespace
    if kubectl --context "$context" get namespace "tenant-$TENANT_NAME" &>/dev/null; then
        echo -e "${GREEN}${CHECK} Namespace ready${NC}"
    else
        echo -e "${RED}${CROSS} Namespace not found${NC}"
        return 1
    fi
    
    # Check quota
    if kubectl --context "$context" get resourcequota "$TENANT_NAME-quota" -n "tenant-$TENANT_NAME" &>/dev/null; then
        echo -e "${GREEN}${CHECK} Resource quota applied${NC}"
    else
        echo -e "${YELLOW}⚠️ Resource quota not found${NC}"
    fi
    
    # Check RBAC
    if kubectl --context "$context" get role "$TENANT_NAME-role" -n "tenant-$TENANT_NAME" &>/dev/null; then
        echo -e "${GREEN}${CHECK} RBAC configured${NC}"
    else
        echo -e "${YELLOW}⚠️ RBAC not found${NC}"
    fi
    
    # Check network policy
    local policy_name="$TENANT_NAME-network-isolation"
    if [[ "$TENANT_TIER" == "isolated" ]]; then
        policy_name="$TENANT_NAME-strict-isolation"
    fi
    
    if kubectl --context "$context" get networkpolicy "$policy_name" -n "tenant-$TENANT_NAME" &>/dev/null; then
        echo -e "${GREEN}${CHECK} Network isolation configured${NC}"
    else
        echo -e "${YELLOW}⚠️ Network policy not found${NC}"
    fi
    
    echo -e "${GREEN}${CHECK} Tenant verification complete${NC}"
}

show_tenant_info() {
    local context="$SHARED_CONTEXT"
    if [[ "$TENANT_TIER" == "isolated" ]]; then
        context="$ISOLATED_CONTEXT"
    fi
    
    echo ""
    echo -e "${BOLD}${GREEN}${CHECK} Tenant '$TENANT_NAME' created successfully!${NC}"
    echo ""
    echo -e "${CYAN}Tenant Information:${NC}"
    echo -e "  • Name: $TENANT_NAME"
    echo -e "  • Tier: $TENANT_TIER"
    echo -e "  • Cluster: $(echo $context | sed 's/kind-//')"
    echo -e "  • Namespace: tenant-$TENANT_NAME"
    echo ""
    echo -e "${CYAN}Available Commands:${NC}"
    echo -e "  ${BOLD}kubectl --context $context get all -n tenant-$TENANT_NAME${NC}"
    echo -e "  ${BOLD}kubectl --context $context get resourcequota -n tenant-$TENANT_NAME${NC}"
    echo -e "  ${BOLD}kubectl --context $context get networkpolicy -n tenant-$TENANT_NAME${NC}"
    echo ""
    echo -e "${CYAN}Management Commands:${NC}"
    echo -e "  ${BOLD}./delete-tenant.sh $TENANT_NAME${NC}          Remove this tenant"
    echo -e "  ${BOLD}./monitor-tenants.sh${NC}                     Monitor all tenants"
    echo -e "  ${BOLD}./tenant-resource-report.sh${NC}              Generate usage report"
    echo ""
}

# Main execution
main() {
    print_header
    
    echo -e "${CYAN}Creating new tenant with the following configuration:${NC}"
    echo -e "  • Tenant Name: $TENANT_NAME"
    echo -e "  • Tenant Tier: $TENANT_TIER"
    echo -e "  • Target Cluster: $(if [[ "$TENANT_TIER" == "isolated" ]]; then echo "tenant-isolated"; else echo "tenant-shared"; fi)"
    echo ""
    
    validate_tenant_name
    create_tenant_namespace
    create_tenant_resources
    deploy_tenant_application
    verify_tenant_creation
    show_tenant_info
}

# Check for help
if [[ "${1:-}" == "help" || "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
    echo "TMC Create Tenant Script"
    echo ""
    echo "Creates a new tenant workspace with proper isolation and resources."
    echo ""
    echo "Usage: $0 <tenant-name> [shared|isolated]"
    echo ""
    echo "Parameters:"
    echo "  tenant-name    Name of the tenant (lowercase, alphanumeric, hyphens)"
    echo "  tier          Tenant tier: 'shared' (default) or 'isolated'"
    echo ""
    echo "Examples:"
    echo "  $0 new-company shared      Create shared tenant"
    echo "  $0 enterprise-client isolated   Create isolated tenant"
    echo ""
    exit 0
fi

# Run the script
main "$@"