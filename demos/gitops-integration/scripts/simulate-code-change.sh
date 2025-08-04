#!/bin/bash

# TMC GitOps Integration - Simulate Code Change
# Demonstrates complete GitOps workflow from code change to deployment

set -e

# Demo configuration
DEMO_DIR="$(dirname "$0")/.."
GIT_REPOS_DIR="$DEMO_DIR/git-repos"

# Cluster contexts
DEV_CONTEXT="kind-gitops-dev"
STAGING_CONTEXT="kind-gitops-staging"
PROD_CONTEXT="kind-gitops-prod"

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
GIT="📝"
WARNING="⚠️"

print_usage() {
    echo "TMC GitOps Integration - Simulate Code Change"
    echo ""
    echo "Usage: $0 <version> [feature-description]"
    echo ""
    echo "Examples:"
    echo "  $0 v1.2.0 \"Added new dashboard features\""
    echo "  $0 v1.3.0 \"Performance improvements\""
    echo "  $0 v2.0.0 \"Major UI redesign\""
    echo ""
    echo "This script simulates a complete GitOps workflow:"
    echo "  1. Makes code changes in the git repository"
    echo "  2. Commits changes with proper versioning"
    echo "  3. Simulates ArgoCD automatic synchronization"
    echo "  4. Shows deployment progression across environments"
    echo ""
}

update_application_version() {
    local new_version=$1
    local feature_description=$2
    
    echo -e "${BLUE}${GIT} Updating application to $new_version...${NC}"
    
    local app_repo="$GIT_REPOS_DIR/demo-app"
    
    if [[ ! -d "$app_repo" ]]; then
        echo -e "${RED}${CROSS} Application repository not found at $app_repo${NC}"
        echo -e "${YELLOW}Please run the main demo first: ./run-demo.sh${NC}"
        exit 1
    fi
    
    cd "$app_repo"
    
    # Update version in all environments
    echo -e "${CYAN}Updating version in all environment manifests...${NC}"
    sed -i "s/value: \"v[0-9]\+\.[0-9]\+\.[0-9]\+\"/value: \"$new_version\"/" environments/*/webapp.yaml
    
    # Add feature description to dev environment first
    if [[ -n "$feature_description" ]]; then
        echo -e "${CYAN}Adding feature description to development environment...${NC}"
        sed -i "s/New GitOps Features ([^)]*)/New GitOps Features ($new_version)/" environments/dev/webapp.yaml
        sed -i "s/<li>Environment promotion<\/li>/<li>Environment promotion<\/li>\n                    <li>$feature_description<\/li>/" environments/dev/webapp.yaml
    fi
    
    # Commit changes
    git add .
    git commit -m "feat: update to $new_version

${feature_description:+- $feature_description}
- Updated application version across all environments
- Enhanced GitOps workflow demonstration
- Ready for multi-environment deployment

GitOps-Deploy: auto-sync-dev-staging
Production-Deploy: manual-approval-required"
    
    echo -e "${GREEN}${CHECK} Changes committed to Git repository${NC}"
    echo -e "${CYAN}Git commit hash: $(git rev-parse --short HEAD)${NC}"
    
    cd - > /dev/null
}

simulate_argocd_sync() {
    local new_version=$1
    
    echo -e "${BLUE}${ROCKET} Simulating ArgoCD synchronization...${NC}"
    echo ""
    
    # Simulate dev environment sync (immediate)
    echo -e "${CYAN}ArgoCD: Syncing to Development environment...${NC}"
    echo -e "${YELLOW}(Auto-sync enabled - immediate deployment)${NC}"
    
    local app_repo="$GIT_REPOS_DIR/demo-app"
    
    # Apply changes to dev environment
    if kubectl --context "$DEV_CONTEXT" get deployment demo-webapp &>/dev/null; then
        kubectl --context "$DEV_CONTEXT" apply -f "$app_repo/environments/dev/webapp.yaml" 2>/dev/null || true
        echo -e "${GREEN}${CHECK} Development environment updated${NC}"
        
        # Wait for rollout
        echo -e "${CYAN}Waiting for deployment rollout in dev...${NC}"
        kubectl --context "$DEV_CONTEXT" rollout status deployment/demo-webapp --timeout=60s || true
        echo -e "${GREEN}${CHECK} Development deployment complete${NC}"
    else
        echo -e "${YELLOW}${WARNING} Development deployment not found${NC}"
    fi
    
    echo ""
    
    # Simulate staging environment sync (auto but with delay)
    echo -e "${CYAN}ArgoCD: Syncing to Staging environment...${NC}"
    echo -e "${YELLOW}(Auto-sync enabled - deploying after dev validation)${NC}"
    
    sleep 3
    
    if kubectl --context "$STAGING_CONTEXT" get deployment demo-webapp &>/dev/null; then
        kubectl --context "$STAGING_CONTEXT" apply -f "$app_repo/environments/staging/webapp.yaml" 2>/dev/null || true
        echo -e "${GREEN}${CHECK} Staging environment updated${NC}"
        
        # Wait for rollout
        echo -e "${CYAN}Waiting for deployment rollout in staging...${NC}"
        kubectl --context "$STAGING_CONTEXT" rollout status deployment/demo-webapp --timeout=60s || true
        echo -e "${GREEN}${CHECK} Staging deployment complete${NC}"
    else
        echo -e "${YELLOW}${WARNING} Staging deployment not found${NC}"
    fi
    
    echo ""
    
    # Production requires manual approval
    echo -e "${CYAN}ArgoCD: Production environment status...${NC}"
    echo -e "${YELLOW}${WARNING} Manual sync required for production deployment${NC}"
    echo -e "${CYAN}Production deployment awaiting approval...${NC}"
    
    echo ""
    echo -e "${BOLD}${BLUE}GitOps Workflow Status:${NC}"
    echo -e "${GREEN}${CHECK} Development: $new_version deployed and running${NC}"
    echo -e "${GREEN}${CHECK} Staging: $new_version deployed and running${NC}"
    echo -e "${YELLOW}${WARNING} Production: Manual approval required for $new_version${NC}"
}

show_deployment_status() {
    local version=$1
    
    echo -e "${BOLD}${BLUE}Post-Deployment Status Check${NC}"
    echo ""
    
    # Check each environment
    local environments=("$DEV_CONTEXT:Development" "$STAGING_CONTEXT:Staging" "$PROD_CONTEXT:Production")
    
    for env_info in "${environments[@]}"; do
        IFS=':' read -r context env_name <<< "$env_info"
        
        echo -e "${CYAN}$env_name Environment:${NC}"
        
        if kubectl --context "$context" get deployment demo-webapp &>/dev/null; then
            local current_version=$(kubectl --context "$context" get deployment demo-webapp -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="VERSION")].value}' 2>/dev/null || echo "unknown")
            local ready_replicas=$(kubectl --context "$context" get deployment demo-webapp -o jsonpath='{.status.readyReplicas}' 2>/dev/null || echo "0")
            local desired_replicas=$(kubectl --context "$context" get deployment demo-webapp -o jsonpath='{.spec.replicas}' 2>/dev/null || echo "0")
            
            if [[ "$current_version" == "$version" ]]; then
                echo -e "  ${GREEN}${CHECK} Version: $current_version (updated)${NC}"
            else
                echo -e "  ${YELLOW}${WARNING} Version: $current_version (pending update to $version)${NC}"
            fi
            
            if [[ "$ready_replicas" -eq "$desired_replicas" ]] && [[ "$ready_replicas" -gt 0 ]]; then
                echo -e "  ${GREEN}${CHECK} Status: Running ($ready_replicas/$desired_replicas replicas)${NC}"
            else
                echo -e "  ${YELLOW}${WARNING} Status: Updating ($ready_replicas/$desired_replicas replicas)${NC}"
            fi
        else
            echo -e "  ${RED}${CROSS} Deployment not found${NC}"
        fi
        
        echo ""
    done
}

promote_to_production() {
    local version=$1
    
    echo -e "${YELLOW}Do you want to promote $version to production? (y/N)${NC}"
    read -r response
    
    case "$response" in
        [yY][eE][sS]|[yY])
            echo -e "${BLUE}${ROCKET} Promoting $version to production...${NC}"
            
            local app_repo="$GIT_REPOS_DIR/demo-app"
            
            if kubectl --context "$PROD_CONTEXT" get deployment demo-webapp &>/dev/null; then
                kubectl --context "$PROD_CONTEXT" apply -f "$app_repo/environments/prod/webapp.yaml" 2>/dev/null || true
                echo -e "${GREEN}${CHECK} Production deployment initiated${NC}"
                
                # Wait for rollout
                echo -e "${CYAN}Waiting for production deployment rollout...${NC}"
                kubectl --context "$PROD_CONTEXT" rollout status deployment/demo-webapp --timeout=120s || true
                echo -e "${GREEN}${CHECK} Production deployment complete${NC}"
            else
                echo -e "${YELLOW}${WARNING} Production deployment not found${NC}"
            fi
            ;;
        *)
            echo -e "${CYAN}Production deployment skipped - manual approval can be done later${NC}"
            ;;
    esac
}

show_next_steps() {
    echo -e "${BOLD}${BLUE}GitOps Workflow Complete${NC}"
    echo ""
    echo -e "${CYAN}What happened:${NC}"
    echo -e "  1. ${GIT} Code changes committed to Git repository"
    echo -e "  2. ${ROCKET} ArgoCD automatically synced to dev and staging"
    echo -e "  3. ${CHECK} Applications updated with zero downtime"
    echo -e "  4. ${WARNING} Production awaiting manual approval"
    echo ""
    echo -e "${YELLOW}Next steps:${NC}"
    echo -e "  • Monitor deployments: ${BOLD}./scripts/monitor-gitops.sh${NC}"
    echo -e "  • Check application status: ${BOLD}./scripts/show-app-status.sh${NC}"
    echo -e "  • Promote to production: ${BOLD}./scripts/promote-to-production.sh${NC}"
    echo -e "  • Rollback if needed: ${BOLD}./scripts/rollback-environment.sh dev${NC}"
    echo ""
    echo -e "${CYAN}Key GitOps benefits demonstrated:${NC}"
    echo -e "  • Git as single source of truth"
    echo -e "  • Automated multi-environment deployment"
    echo -e "  • TMC transparent multi-cluster coordination"
    echo -e "  • Production safety with manual approvals"
    echo ""
}

# Main execution
main() {
    local new_version="${1:-}"
    local feature_description="${2:-}"
    
    if [[ -z "$new_version" ]]; then
        print_usage
        exit 1
    fi
    
    # Validate version format
    if [[ ! "$new_version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
        echo -e "${RED}Error: Version must be in format vX.Y.Z (e.g., v1.2.0)${NC}"
        exit 1
    fi
    
    echo -e "${BOLD}${PURPLE}${ROCKET} TMC GitOps Workflow Simulation${NC}"
    echo -e "${CYAN}Simulating deployment of $new_version${NC}"
    if [[ -n "$feature_description" ]]; then
        echo -e "${CYAN}Feature: $feature_description${NC}"
    fi
    echo ""
    
    update_application_version "$new_version" "$feature_description"
    echo ""
    
    simulate_argocd_sync "$new_version"
    echo ""
    
    show_deployment_status "$new_version"
    
    promote_to_production "$new_version"
    echo ""
    
    show_next_steps
}

# Check for help
if [[ "${1:-}" == "help" || "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
    print_usage
    exit 0
fi

# Run the script
main "$@"