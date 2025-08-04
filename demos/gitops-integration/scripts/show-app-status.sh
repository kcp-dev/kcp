#!/bin/bash

# TMC GitOps Integration - Application Status Display
# Shows current status of applications across all environments

set -e

# Cluster contexts
KCP_CONTEXT="kind-gitops-kcp"
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
WARNING="⚠️"
CLUSTER="🔗"
GIT="📝"

print_header() {
    echo -e "${BOLD}${PURPLE}===============================================${NC}"
    echo -e "${BOLD}${PURPLE}${GIT} TMC GitOps Application Status${NC}"
    echo -e "${BOLD}${PURPLE}===============================================${NC}"
    echo -e "${CYAN}$(date)${NC}"
    echo ""
}

check_application_status() {
    local context=$1
    local environment=$2
    local app_name="demo-webapp"
    
    echo -e "${BOLD}${BLUE}$environment Environment${NC}"
    echo -e "${CYAN}Cluster: $context${NC}"
    
    # Check if cluster is accessible
    if ! kubectl --context "$context" get nodes &>/dev/null; then
        echo -e "${RED}${CROSS} Cluster: UNREACHABLE${NC}"
        echo ""
        return 1
    fi
    
    # Check deployment status
    if kubectl --context "$context" get deployment "$app_name" &>/dev/null; then
        local ready_replicas=$(kubectl --context "$context" get deployment "$app_name" -o jsonpath='{.status.readyReplicas}' 2>/dev/null || echo "0")
        local desired_replicas=$(kubectl --context "$context" get deployment "$app_name" -o jsonpath='{.spec.replicas}' 2>/dev/null || echo "0")
        local image=$(kubectl --context "$context" get deployment "$app_name" -o jsonpath='{.spec.template.spec.containers[0].image}' 2>/dev/null || echo "unknown")
        
        if [[ "$ready_replicas" -eq "$desired_replicas" ]] && [[ "$ready_replicas" -gt 0 ]]; then
            echo -e "${GREEN}${CHECK} Application: Running ($ready_replicas/$desired_replicas replicas)${NC}"
        else
            echo -e "${YELLOW}${WARNING} Application: Starting ($ready_replicas/$desired_replicas replicas)${NC}"
        fi
        
        echo -e "${CYAN}Image: $image${NC}"
        
        # Check application version from environment variable
        local version=$(kubectl --context "$context" get deployment "$app_name" -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="VERSION")].value}' 2>/dev/null || echo "unknown")
        echo -e "${CYAN}Version: $version${NC}"
        
        # Check service
        if kubectl --context "$context" get service demo-webapp-svc &>/dev/null; then
            local service_ip=$(kubectl --context "$context" get service demo-webapp-svc -o jsonpath='{.spec.clusterIP}' 2>/dev/null || echo "unknown")
            echo -e "${GREEN}${CHECK} Service: Available ($service_ip:80)${NC}"
        else
            echo -e "${RED}${CROSS} Service: Not found${NC}"
        fi
        
        # Check pods
        local pods_running=$(kubectl --context "$context" get pods -l app=demo-webapp --no-headers 2>/dev/null | grep -c "Running" || echo "0")
        local total_pods=$(kubectl --context "$context" get pods -l app=demo-webapp --no-headers 2>/dev/null | wc -l || echo "0")
        
        if [[ $pods_running -eq $total_pods ]] && [[ $total_pods -gt 0 ]]; then
            echo -e "${GREEN}${CHECK} Pods: $pods_running/$total_pods running${NC}"
        else
            echo -e "${YELLOW}${WARNING} Pods: $pods_running/$total_pods running${NC}"
        fi
        
        # Show pod details
        if [[ $total_pods -gt 0 ]]; then
            echo -e "${CYAN}Pod Status:${NC}"
            kubectl --context "$context" get pods -l app=demo-webapp --no-headers 2>/dev/null | while read -r name ready status restarts age; do
                if [[ "$status" == "Running" ]]; then
                    echo -e "  ${GREEN}${CHECK} $name: $status ($ready)${NC}"
                else
                    echo -e "  ${YELLOW}${WARNING} $name: $status ($ready)${NC}"
                fi
            done || echo -e "  ${YELLOW}${WARNING} Unable to get pod details${NC}"
        fi
        
    else
        echo -e "${RED}${CROSS} Application: Not deployed${NC}"
    fi
    
    echo ""
}

check_argocd_status() {
    echo -e "${BOLD}${BLUE}ArgoCD Status (KCP Cluster)${NC}"
    echo -e "${CYAN}Cluster: $KCP_CONTEXT${NC}"
    
    if ! kubectl --context "$KCP_CONTEXT" get nodes &>/dev/null; then
        echo -e "${RED}${CROSS} KCP Cluster: UNREACHABLE${NC}"
        echo ""
        return 1
    fi
    
    # Check ArgoCD components
    local components=("argocd-server" "argocd-repo-server" "argocd-application-controller")
    
    for component in "${components[@]}"; do
        if kubectl --context "$KCP_CONTEXT" get deployment "$component" -n argocd &>/dev/null; then
            local ready_replicas=$(kubectl --context "$KCP_CONTEXT" get deployment "$component" -n argocd -o jsonpath='{.status.readyReplicas}' 2>/dev/null || echo "0")
            local desired_replicas=$(kubectl --context "$KCP_CONTEXT" get deployment "$component" -n argocd -o jsonpath='{.spec.replicas}' 2>/dev/null || echo "0")
            
            if [[ "$ready_replicas" -eq "$desired_replicas" ]] && [[ "$ready_replicas" -gt 0 ]]; then
                echo -e "${GREEN}${CHECK} $component: Running ($ready_replicas/$desired_replicas)${NC}"
            else
                echo -e "${YELLOW}${WARNING} $component: Starting ($ready_replicas/$desired_replicas)${NC}"
            fi
        else
            echo -e "${RED}${CROSS} $component: Not found${NC}"
        fi
    done
    
    # Check ArgoCD Applications
    echo -e "${CYAN}ArgoCD Applications:${NC}"
    if kubectl --context "$KCP_CONTEXT" get applications -n argocd &>/dev/null; then
        local apps=$(kubectl --context "$KCP_CONTEXT" get applications -n argocd --no-headers 2>/dev/null | wc -l || echo "0")
        if [[ $apps -gt 0 ]]; then
            echo -e "${GREEN}${CHECK} Applications: $apps configured${NC}"
            kubectl --context "$KCP_CONTEXT" get applications -n argocd --no-headers 2>/dev/null | while read -r name health sync age; do
                echo -e "  • $name: Health=$health, Sync=$sync"
            done || true
        else
            echo -e "${YELLOW}${WARNING} Applications: None found${NC}"
        fi
    else
        echo -e "${RED}${CROSS} Applications: Unable to check${NC}"
    fi
    
    echo ""
}

check_tmc_syncers() {
    echo -e "${BOLD}${BLUE}TMC Syncer Status${NC}"
    
    local environments=("$DEV_CONTEXT:Development" "$STAGING_CONTEXT:Staging" "$PROD_CONTEXT:Production")
    
    for env_info in "${environments[@]}"; do
        IFS=':' read -r context env_name <<< "$env_info"
        
        echo -e "${CYAN}$env_name Syncer:${NC}"
        
        if ! kubectl --context "$context" get nodes &>/dev/null; then
            echo -e "${RED}${CROSS} Cluster unreachable${NC}"
            continue
        fi
        
        if kubectl --context "$context" get deployment kcp-syncer &>/dev/null; then
            local ready_replicas=$(kubectl --context "$context" get deployment kcp-syncer -o jsonpath='{.status.readyReplicas}' 2>/dev/null || echo "0")
            local desired_replicas=$(kubectl --context "$context" get deployment kcp-syncer -o jsonpath='{.spec.replicas}' 2>/dev/null || echo "0")
            
            if [[ "$ready_replicas" -eq "$desired_replicas" ]] && [[ "$ready_replicas" -gt 0 ]]; then
                echo -e "  ${GREEN}${CHECK} Syncer: Connected ($ready_replicas/$desired_replicas)${NC}"
            else
                echo -e "  ${YELLOW}${WARNING} Syncer: Starting ($ready_replicas/$desired_replicas)${NC}"
            fi
        else
            echo -e "  ${RED}${CROSS} Syncer: Not deployed${NC}"
        fi
    done
    
    echo ""
}

show_deployment_summary() {
    echo -e "${BOLD}${BLUE}Deployment Summary${NC}"
    echo -e "┌─────────────────┬─────────────┬─────────────┬─────────────────┐"
    echo -e "│ Environment     │ App Status  │ Version     │ Sync Method     │"
    echo -e "├─────────────────┼─────────────┼─────────────┼─────────────────┤"
    
    # Check each environment
    local environments=("$DEV_CONTEXT:Development:auto" "$STAGING_CONTEXT:Staging:auto" "$PROD_CONTEXT:Production:manual")
    
    for env_info in "${environments[@]}"; do
        IFS=':' read -r context env_name sync_method <<< "$env_info"
        
        local app_status="Unknown"
        local version="Unknown"
        
        if kubectl --context "$context" get nodes &>/dev/null; then
            if kubectl --context "$context" get deployment demo-webapp &>/dev/null; then
                local ready_replicas=$(kubectl --context "$context" get deployment demo-webapp -o jsonpath='{.status.readyReplicas}' 2>/dev/null || echo "0")
                local desired_replicas=$(kubectl --context "$context" get deployment demo-webapp -o jsonpath='{.spec.replicas}' 2>/dev/null || echo "0")
                
                if [[ "$ready_replicas" -eq "$desired_replicas" ]] && [[ "$ready_replicas" -gt 0 ]]; then
                    app_status="${GREEN}Running${NC}"
                else
                    app_status="${YELLOW}Starting${NC}"
                fi
                
                version=$(kubectl --context "$context" get deployment demo-webapp -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="VERSION")].value}' 2>/dev/null || echo "unknown")
            else
                app_status="${RED}Not Deployed${NC}"
            fi
        else
            app_status="${RED}Unreachable${NC}"
        fi
        
        printf "│ %-15s │ %-11s │ %-11s │ %-15s │\n" "$env_name" "$app_status" "$version" "$sync_method"
    done
    
    echo -e "└─────────────────┴─────────────┴─────────────┴─────────────────┘"
    echo ""
}

# Main execution
main() {
    print_header
    
    check_argocd_status
    check_tmc_syncers
    
    # Check each environment
    check_application_status "$DEV_CONTEXT" "Development"
    check_application_status "$STAGING_CONTEXT" "Staging"
    check_application_status "$PROD_CONTEXT" "Production"
    
    show_deployment_summary
    
    echo -e "${BOLD}${CYAN}GitOps Workflow Status:${NC}"
    echo -e "  • ${GREEN}Development:${NC} Auto-sync enabled, immediate deployment"
    echo -e "  • ${YELLOW}Staging:${NC} Auto-sync enabled, promotes from dev"
    echo -e "  • ${RED}Production:${NC} Manual sync required, approval-based deployment"
    echo ""
    echo -e "${CYAN}For real-time monitoring, run:${NC}"
    echo -e "${YELLOW}  ./scripts/monitor-gitops.sh${NC}"
    echo ""
}

# Run the script
main "$@"