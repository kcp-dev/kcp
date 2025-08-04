#!/bin/bash

# TMC GitOps Integration - Real-time GitOps Monitor
# Provides live dashboard showing GitOps workflow status across all environments

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
DIM='\033[2m'
NC='\033[0m' # No Color

# Unicode symbols
CHECK="✅"
CROSS="❌"
WARNING="⚠️"
ROCKET="🚀"
CLUSTER="🔗"
GIT="📝"
SYNC="🔄"
MANUAL="✋"

# Clear screen function
clear_screen() {
    printf '\033[2J\033[H'
}

# Print header
print_header() {
    echo -e "${BOLD}${PURPLE}===============================================================${NC}"
    echo -e "${BOLD}${PURPLE}${GIT} TMC GitOps Integration Monitor${NC}"
    echo -e "${BOLD}${PURPLE}===============================================================${NC}"
    echo -e "${DIM}Last updated: $(date) | Press Ctrl+C to stop${NC}"
    echo ""
}

# Check if cluster is healthy
check_cluster_health() {
    local context=$1
    
    if ! kubectl --context "$context" get nodes &>/dev/null; then
        echo "false"
        return
    fi
    
    local nodes_ready=$(kubectl --context "$context" get nodes --no-headers 2>/dev/null | grep -c "Ready" || echo "0")
    local total_nodes=$(kubectl --context "$context" get nodes --no-headers 2>/dev/null | wc -l || echo "0")
    
    if [[ $nodes_ready -eq $total_nodes ]] && [[ $total_nodes -gt 0 ]]; then
        echo "true"
    else
        echo "false"
    fi
}

# Check application health
check_app_health() {
    local context=$1
    local deployment=$2
    local namespace="${3:-default}"
    
    if ! kubectl --context "$context" get deployment "$deployment" -n "$namespace" &>/dev/null; then
        echo "not-found"
        return
    fi
    
    local ready_replicas=$(kubectl --context "$context" get deployment "$deployment" -n "$namespace" -o jsonpath='{.status.readyReplicas}' 2>/dev/null || echo "0")
    local desired_replicas=$(kubectl --context "$context" get deployment "$deployment" -n "$namespace" -o jsonpath='{.spec.replicas}' 2>/dev/null || echo "0")
    
    if [[ "$ready_replicas" -eq "$desired_replicas" ]] && [[ "$ready_replicas" -gt 0 ]]; then
        echo "healthy:$ready_replicas"
    elif [[ "$ready_replicas" -lt "$desired_replicas" ]]; then
        echo "starting:$ready_replicas/$desired_replicas"
    else
        echo "unhealthy:$ready_replicas"
    fi
}

# Get application version
get_app_version() {
    local context=$1
    local deployment=$2
    local namespace="${3:-default}"
    
    if ! kubectl --context "$context" get deployment "$deployment" -n "$namespace" &>/dev/null; then
        echo "unknown"
        return
    fi
    
    local version=$(kubectl --context "$context" get deployment "$deployment" -n "$namespace" -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="VERSION")].value}' 2>/dev/null || echo "unknown")
    echo "$version"
}

# Show ArgoCD status
show_argocd_status() {
    echo -e "${BOLD}${BLUE}ArgoCD Control Plane${NC}"
    echo -e "┌─────────────────┬─────────────┬─────────────┬─────────────────┐"
    echo -e "│ Component       │ Status      │ Replicas    │ Function        │"
    echo -e "├─────────────────┼─────────────┼─────────────┼─────────────────┤"
    
    local components=("argocd-server:Server" "argocd-repo-server:Repo Server" "argocd-application-controller:App Controller")
    
    for component_info in "${components[@]}"; do
        IFS=':' read -r component display_name <<< "$component_info"
        
        local health=$(check_app_health "$KCP_CONTEXT" "$component" "argocd")
        local status_display=""
        local replicas_display=""
        
        case "$health" in
            "healthy:"*)
                replicas_count="${health#healthy:}"
                status_display="${GREEN}${CHECK} Running${NC}"
                replicas_display="${replicas_count}/1"
                ;;
            "starting:"*)
                replicas_info="${health#starting:}"
                status_display="${YELLOW}${WARNING} Starting${NC}"
                replicas_display="$replicas_info"
                ;;
            "not-found")
                status_display="${RED}${CROSS} Not Found${NC}"
                replicas_display="0/0"
                ;;
            *)
                status_display="${RED}${CROSS} Unhealthy${NC}"
                replicas_display="?/?"
                ;;
        esac
        
        printf "│ %-15s │ %-11s │ %-11s │ %-15s │\n" "$display_name" "$status_display" "$replicas_display" "GitOps Management"
    done
    
    echo -e "└─────────────────┴─────────────┴─────────────┴─────────────────┘"
    echo ""
}

# Show environment status
show_environment_status() {
    echo -e "${BOLD}${BLUE}Multi-Environment Application Status${NC}"
    echo -e "┌─────────────────┬─────────────┬─────────────┬─────────────┬─────────────────┐"
    echo -e "│ Environment     │ App Status  │ Replicas    │ Version     │ Sync Method     │"
    echo -e "├─────────────────┼─────────────┼─────────────┼─────────────┼─────────────────┤"
    
    local environments=("$DEV_CONTEXT:Development:${SYNC} Auto" "$STAGING_CONTEXT:Staging:${SYNC} Auto" "$PROD_CONTEXT:Production:${MANUAL} Manual")
    
    for env_info in "${environments[@]}"; do
        IFS=':' read -r context env_name sync_method <<< "$env_info"
        
        local cluster_healthy=$(check_cluster_health "$context")
        local app_health=$(check_app_health "$context" "demo-webapp")
        local app_version=$(get_app_version "$context" "demo-webapp")
        
        local status_display=""
        local replicas_display=""
        
        if [[ "$cluster_healthy" == "false" ]]; then
            status_display="${RED}${CROSS} Unreachable${NC}"
            replicas_display="?/?"
            app_version="unknown"
        else
            case "$app_health" in
                "healthy:"*)
                    replicas_count="${app_health#healthy:}"
                    status_display="${GREEN}${CHECK} Running${NC}"
                    replicas_display="${replicas_count}/2"
                    if [[ "$env_name" == "Production" ]]; then
                        replicas_display="${replicas_count}/3"  # Prod has 3 replicas
                    fi
                    ;;
                "starting:"*)
                    replicas_info="${app_health#starting:}"
                    status_display="${YELLOW}${WARNING} Starting${NC}"
                    replicas_display="$replicas_info"
                    ;;
                "not-found")
                    status_display="${YELLOW}${WARNING} Not Deployed${NC}"
                    replicas_display="0/0"
                    app_version="none"
                    ;;
                *)
                    status_display="${RED}${CROSS} Unhealthy${NC}"
                    replicas_display="?/?"
                    ;;
            esac
        fi
        
        printf "│ %-15s │ %-11s │ %-11s │ %-11s │ %-15s │\n" "$env_name" "$status_display" "$replicas_display" "$app_version" "$sync_method"
    done
    
    echo -e "└─────────────────┴─────────────┴─────────────┴─────────────┴─────────────────┘"
    echo ""
}

# Show TMC syncer status
show_syncer_status() {
    echo -e "${BOLD}${BLUE}TMC Syncer Coordination${NC}"
    echo -e "┌─────────────────┬─────────────┬─────────────┬─────────────────┐"
    echo -e "│ Environment     │ Syncer      │ Connection  │ GitOps Mode     │"
    echo -e "├─────────────────┼─────────────┼─────────────┼─────────────────┤"
    
    local environments=("$DEV_CONTEXT:Development" "$STAGING_CONTEXT:Staging" "$PROD_CONTEXT:Production")
    
    for env_info in "${environments[@]}"; do
        IFS=':' read -r context env_name <<< "$env_info"
        
        local cluster_healthy=$(check_cluster_health "$context")
        local syncer_health=$(check_app_health "$context" "kcp-syncer")
        
        local syncer_status=""
        local connection_status=""
        local gitops_mode=""
        
        if [[ "$cluster_healthy" == "false" ]]; then
            syncer_status="${RED}${CROSS} Down${NC}"
            connection_status="${RED}Disconnected${NC}"
            gitops_mode="Unknown"
        else
            case "$syncer_health" in
                "healthy:"*)
                    syncer_status="${GREEN}${CHECK} Running${NC}"
                    connection_status="${GREEN}Connected${NC}"
                    gitops_mode="Enabled"
                    ;;
                "starting:"*)
                    syncer_status="${YELLOW}${WARNING} Starting${NC}"
                    connection_status="${YELLOW}Connecting${NC}"
                    gitops_mode="Initializing"
                    ;;
                "not-found")
                    syncer_status="${RED}${CROSS} Missing${NC}"
                    connection_status="${RED}No Syncer${NC}"
                    gitops_mode="Disabled"
                    ;;
                *)
                    syncer_status="${RED}${CROSS} Failed${NC}"
                    connection_status="${RED}Failed${NC}"
                    gitops_mode="Error"
                    ;;
            esac
        fi
        
        printf "│ %-15s │ %-11s │ %-11s │ %-15s │\n" "$env_name" "$syncer_status" "$connection_status" "$gitops_mode"
    done
    
    echo -e "└─────────────────┴─────────────┴─────────────┴─────────────────┘"
    echo ""
}

# Show GitOps workflow status
show_gitops_workflow() {
    echo -e "${BOLD}${BLUE}GitOps Workflow Pipeline${NC}"
    
    # Get application versions from each environment
    local dev_version=$(get_app_version "$DEV_CONTEXT" "demo-webapp")
    local staging_version=$(get_app_version "$STAGING_CONTEXT" "demo-webapp")
    local prod_version=$(get_app_version "$PROD_CONTEXT" "demo-webapp")
    
    echo -e "${CYAN}Code → Git → ArgoCD → Multi-Cluster Deployment${NC}"
    echo ""
    
    # Show deployment pipeline
    echo -e "┌─────────────────┬─────────────┬─────────────┬─────────────────┐"
    echo -e "│ Stage           │ Version     │ Status      │ Next Action     │"
    echo -e "├─────────────────┼─────────────┼─────────────┼─────────────────┤"
    
    # Git repository status
    echo -e "│ Git Repository  │ v1.1.0      │ ${GREEN}${CHECK} Latest${NC}  │ Auto-sync       │"
    
    # Development
    local dev_status="${GREEN}${CHECK} Synced${NC}"
    local dev_action="Auto-promote"
    if [[ "$dev_version" != "v1.1.0" ]]; then
        dev_status="${YELLOW}${WARNING} Syncing${NC}"
        dev_action="Deploying..."
    fi
    printf "│ %-15s │ %-11s │ %-11s │ %-15s │\n" "Development" "$dev_version" "$dev_status" "$dev_action"
    
    # Staging
    local staging_status="${GREEN}${CHECK} Synced${NC}"
    local staging_action="Ready"
    if [[ "$staging_version" != "v1.1.0" ]]; then
        staging_status="${YELLOW}${WARNING} Syncing${NC}"
        staging_action="Deploying..."
    fi
    printf "│ %-15s │ %-11s │ %-11s │ %-15s │\n" "Staging" "$staging_version" "$staging_status" "$staging_action"
    
    # Production
    local prod_status="${YELLOW}${WARNING} Manual${NC}"
    local prod_action="Awaiting Approval"
    if [[ "$prod_version" == "v1.1.0" ]]; then
        prod_status="${GREEN}${CHECK} Deployed${NC}"
        prod_action="Complete"
    fi
    printf "│ %-15s │ %-11s │ %-11s │ %-15s │\n" "Production" "$prod_version" "$prod_status" "$prod_action"
    
    echo -e "└─────────────────┴─────────────┴─────────────┴─────────────────┘"
    echo ""
}

# Show help information
show_help() {
    echo -e "${BOLD}${BLUE}Interactive Commands${NC}"
    echo -e "${CYAN}Available actions:${NC}"
    echo -e "  • Press 'h' for help"
    echo -e "  • Press 's' to simulate code change and deployment"
    echo -e "  • Press 'p' to promote to production"
    echo -e "  • Press 'r' to rollback development"
    echo -e "  • Press 'd' to show detailed application logs"
    echo -e "  • Press 'q' or Ctrl+C to quit"
    echo ""
}

# Simulate code change
simulate_code_change() {
    echo -e "${BLUE}${GIT} Simulating code change: v1.2.0...${NC}"
    
    # This would trigger a real GitOps workflow in production
    echo -e "${CYAN}(In production, this would update Git repo and trigger ArgoCD sync)${NC}"
    
    # For demo, we'll simulate the update
    echo -e "${GREEN}${CHECK} Code committed to Git repository${NC}"
    echo -e "${CYAN}ArgoCD will auto-sync to dev and staging environments...${NC}"
    
    sleep 2
}

# Promote to production
promote_to_production() {
    echo -e "${ROCKET} Promoting to production...${NC}"
    
    # Simulate production deployment
    kubectl --context "$PROD_CONTEXT" patch deployment demo-webapp --type='merge' -p='{"spec":{"template":{"spec":{"containers":[{"name":"webapp","env":[{"name":"VERSION","value":"v1.1.0"}]}]}}}}' &>/dev/null || true
    
    echo -e "${GREEN}${CHECK} Production deployment approved and initiated${NC}"
    sleep 2
}

# Main monitoring loop
monitor_gitops() {
    local refresh_interval=5
    local show_help_flag=false
    
    # Set up input handling
    stty -echo -icanon time 0 min 0
    
    # Trap Ctrl+C for clean exit
    trap 'echo -e "\n${YELLOW}Monitoring stopped.${NC}"; stty echo icanon; exit 0' INT
    
    while true; do
        clear_screen
        print_header
        show_argocd_status
        show_environment_status
        show_syncer_status
        show_gitops_workflow
        
        if [[ "$show_help_flag" == true ]]; then
            show_help
            show_help_flag=false
        fi
        
        echo -e "${DIM}🔄 Updates every ${refresh_interval}s • Press 'h' for help • Ctrl+C to stop${NC}"
        
        # Check for user input
        local input=""
        local count=0
        while [[ $count -lt $refresh_interval ]]; do
            read -t 1 input && break
            count=$((count + 1))
        done
        
        case "$input" in
            "h"|"H")
                show_help_flag=true
                ;;
            "s"|"S")
                simulate_code_change
                ;;
            "p"|"P")
                promote_to_production
                ;;
            "r"|"R")
                echo -e "${YELLOW}Rollback simulation not implemented in demo${NC}"
                sleep 1
                ;;
            "d"|"D")
                echo -e "${CYAN}Detailed logs would be shown in production deployment${NC}"
                sleep 1
                ;;
            "q"|"Q")
                echo -e "\n${YELLOW}Monitoring stopped.${NC}"
                stty echo icanon
                exit 0
                ;;
        esac
    done
}

# Main execution
main() {
    echo -e "${BOLD}${PURPLE}${ROCKET} Starting TMC GitOps Monitor...${NC}"
    echo ""
    echo -e "${CYAN}This monitor provides real-time visibility into:${NC}"
    echo -e "  • ArgoCD application sync status"
    echo -e "  • Multi-environment deployment health"
    echo -e "  • TMC syncer coordination"
    echo -e "  • GitOps workflow pipeline status"
    echo -e "  • Environment promotion workflows"
    echo ""
    echo -e "${YELLOW}Press Enter to start monitoring...${NC}"
    read -r
    
    monitor_gitops
}

# Check for help
if [[ "${1:-}" == "help" || "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
    echo "TMC GitOps Integration Monitor"
    echo ""
    echo "This script provides a real-time dashboard showing:"
    echo "  - ArgoCD application synchronization status"
    echo "  - Multi-environment deployment health"
    echo "  - TMC syncer coordination across clusters"
    echo "  - GitOps workflow pipeline progress"
    echo ""
    echo "Interactive commands available during monitoring:"
    echo "  h - Show help"
    echo "  s - Simulate code change and deployment"
    echo "  p - Promote to production"
    echo "  r - Rollback development"
    echo "  d - Show detailed logs"
    echo "  q - Quit monitoring"
    echo ""
    echo "Usage: $0"
    exit 0
fi

# Run the monitor
main "$@"