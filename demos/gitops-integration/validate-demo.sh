#!/bin/bash

# TMC GitOps Integration Demo - Validation Script
# Validates that all demo components are working correctly

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
ROCKET="🚀"
GIT="📝"

print_header() {
    echo -e "${BOLD}${PURPLE}================================================${NC}"
    echo -e "${BOLD}${PURPLE}🔍 TMC GitOps Integration Demo Validation${NC}"
    echo -e "${BOLD}${PURPLE}================================================${NC}"
    echo ""
}

print_usage() {
    echo "TMC GitOps Integration Demo - Validation Script"
    echo ""
    echo "Usage: $0 [options]"
    echo ""
    echo "Options:"
    echo "  --check-clusters    Validate cluster health"
    echo "  --check-argocd      Validate ArgoCD installation"
    echo "  --check-apps        Validate application deployments"
    echo "  --check-syncers     Validate TMC syncers"
    echo "  --check-gitops      Test GitOps workflow"
    echo "  --check-all         Run all validation checks (default)"
    echo "  --help, -h          Show this help message"
    echo ""
    echo "This script validates:"
    echo "  • Kind cluster accessibility and health"
    echo "  • ArgoCD installation and configuration"
    echo "  • Application deployment status across environments"
    echo "  • TMC syncer connectivity and GitOps integration"
    echo "  • GitOps workflow functionality"
    echo ""
}

validate_cluster() {
    local context=$1
    local cluster_name=$2
    local success=true
    
    echo -e "${BLUE}Validating $cluster_name cluster...${NC}"
    
    # Check cluster accessibility
    if ! kubectl --context "$context" get nodes &>/dev/null; then
        echo -e "${RED}${CROSS} Cluster $cluster_name is not accessible${NC}"
        return 1
    fi
    
    # Check node status
    local nodes_ready=$(kubectl --context "$context" get nodes --no-headers 2>/dev/null | grep -c "Ready" || echo "0")
    local total_nodes=$(kubectl --context "$context" get nodes --no-headers 2>/dev/null | wc -l || echo "0")
    
    if [[ $nodes_ready -eq $total_nodes ]] && [[ $total_nodes -gt 0 ]]; then
        echo -e "${GREEN}${CHECK} Cluster health: $nodes_ready/$total_nodes nodes ready${NC}"
    else
        echo -e "${RED}${CROSS} Cluster health: $nodes_ready/$total_nodes nodes ready${NC}"
        success=false
    fi
    
    # Check system pods
    local system_pods_ready=$(kubectl --context "$context" get pods -n kube-system --no-headers 2>/dev/null | grep -c "Running" || echo "0")
    local total_system_pods=$(kubectl --context "$context" get pods -n kube-system --no-headers 2>/dev/null | wc -l || echo "0")
    
    if [[ $system_pods_ready -eq $total_system_pods ]] && [[ $total_system_pods -gt 0 ]]; then
        echo -e "${GREEN}${CHECK} System pods: $system_pods_ready/$total_system_pods running${NC}"
    else
        echo -e "${YELLOW}${WARNING} System pods: $system_pods_ready/$total_system_pods running${NC}"
    fi
    
    echo ""
    
    if [[ "$success" == "true" ]]; then
        return 0
    else
        return 1
    fi
}

validate_deployment() {
    local context=$1
    local deployment=$2
    local namespace=$3
    local expected_replicas=$4
    
    echo -e "${CYAN}Checking $deployment in $namespace...${NC}"
    
    if ! kubectl --context "$context" get deployment "$deployment" -n "$namespace" &>/dev/null; then
        echo -e "${RED}${CROSS} Deployment $deployment not found in namespace $namespace${NC}"
        return 1
    fi
    
    local ready_replicas=$(kubectl --context "$context" get deployment "$deployment" -n "$namespace" -o jsonpath='{.status.readyReplicas}' 2>/dev/null || echo "0")
    local desired_replicas=$(kubectl --context "$context" get deployment "$deployment" -n "$namespace" -o jsonpath='{.spec.replicas}' 2>/dev/null || echo "0")
    
    if [[ "$ready_replicas" -eq "$desired_replicas" ]] && [[ "$ready_replicas" -ge "$expected_replicas" ]]; then
        echo -e "${GREEN}${CHECK} $deployment: $ready_replicas/$desired_replicas replicas ready${NC}"
        return 0
    else
        echo -e "${RED}${CROSS} $deployment: $ready_replicas/$desired_replicas replicas ready (expected at least $expected_replicas)${NC}"
        return 1
    fi
}

validate_clusters() {
    echo -e "${BOLD}${BLUE}🔍 Cluster Validation${NC}"
    echo ""
    
    local all_healthy=true
    
    # Validate each cluster
    validate_cluster "$KCP_CONTEXT" "KCP Host" || all_healthy=false
    validate_cluster "$DEV_CONTEXT" "Development" || all_healthy=false
    validate_cluster "$STAGING_CONTEXT" "Staging" || all_healthy=false
    validate_cluster "$PROD_CONTEXT" "Production" || all_healthy=false
    
    if [[ "$all_healthy" == "true" ]]; then
        echo -e "${GREEN}${CHECK} All clusters are healthy${NC}"
        return 0
    else
        echo -e "${RED}${CROSS} Some clusters have issues${NC}"
        return 1
    fi
}

validate_argocd() {
    echo -e "${BOLD}${BLUE}${GIT} ArgoCD Validation${NC}"
    echo ""
    
    local all_healthy=true
    
    # Check if ArgoCD namespace exists
    if ! kubectl --context "$KCP_CONTEXT" get namespace argocd &>/dev/null; then
        echo -e "${RED}${CROSS} ArgoCD namespace not found${NC}"
        return 1
    fi
    
    echo -e "${GREEN}${CHECK} ArgoCD namespace exists${NC}"
    
    # Validate ArgoCD components
    echo -e "${CYAN}ArgoCD Components:${NC}"
    validate_deployment "$KCP_CONTEXT" "argocd-server" "argocd" 1 || all_healthy=false
    validate_deployment "$KCP_CONTEXT" "argocd-repo-server" "argocd" 1 || all_healthy=false
    validate_deployment "$KCP_CONTEXT" "argocd-application-controller" "argocd" 1 || all_healthy=false
    echo ""
    
    # Check ArgoCD cluster secrets
    echo -e "${CYAN}ArgoCD Cluster Configuration:${NC}"
    local cluster_secrets=$(kubectl --context "$KCP_CONTEXT" get secrets -n argocd -l argocd.argoproj.io/secret-type=cluster --no-headers 2>/dev/null | wc -l || echo "0")
    
    if [[ $cluster_secrets -ge 3 ]]; then
        echo -e "${GREEN}${CHECK} Cluster secrets: $cluster_secrets configured${NC}"
    else
        echo -e "${RED}${CROSS} Cluster secrets: $cluster_secrets found (expected 3)${NC}"
        all_healthy=false
    fi
    
    # Check ArgoCD Applications
    echo -e "${CYAN}ArgoCD Applications:${NC}"
    if kubectl --context "$KCP_CONTEXT" get applications -n argocd &>/dev/null; then
        local apps=$(kubectl --context "$KCP_CONTEXT" get applications -n argocd --no-headers 2>/dev/null | wc -l || echo "0")
        if [[ $apps -ge 3 ]]; then
            echo -e "${GREEN}${CHECK} Applications: $apps configured${NC}"
            kubectl --context "$KCP_CONTEXT" get applications -n argocd --no-headers 2>/dev/null | while read -r name health sync age; do
                echo -e "  • $name: Health=$health, Sync=$sync"
            done || true
        else
            echo -e "${YELLOW}${WARNING} Applications: $apps found (expected 3)${NC}"
            all_healthy=false
        fi
    else
        echo -e "${RED}${CROSS} Applications: Unable to check${NC}"
        all_healthy=false
    fi
    
    echo ""
    
    if [[ "$all_healthy" == "true" ]]; then
        echo -e "${GREEN}${CHECK} ArgoCD is properly configured${NC}"
        return 0
    else
        echo -e "${RED}${CROSS} ArgoCD has configuration issues${NC}"
        return 1
    fi
}

validate_applications() {
    echo -e "${BOLD}${BLUE}🚀 Application Validation${NC}"
    echo ""
    
    local all_healthy=true
    
    # Validate applications in each environment
    local environments=("$DEV_CONTEXT:Development:2" "$STAGING_CONTEXT:Staging:2" "$PROD_CONTEXT:Production:3")
    
    for env_info in "${environments[@]}"; do
        IFS=':' read -r context env_name expected_replicas <<< "$env_info"
        
        echo -e "${CYAN}$env_name Environment:${NC}"
        validate_deployment "$context" "demo-webapp" "default" "$expected_replicas" || all_healthy=false
        
        # Check service
        if kubectl --context "$context" get service demo-webapp-svc &>/dev/null; then
            local service_ip=$(kubectl --context "$context" get service demo-webapp-svc -o jsonpath='{.spec.clusterIP}' 2>/dev/null || echo "unknown")
            echo -e "${GREEN}${CHECK} Service: demo-webapp-svc ($service_ip)${NC}"
        else
            echo -e "${RED}${CROSS} Service: demo-webapp-svc not found${NC}"
            all_healthy=false
        fi
        
        # Check application version
        local app_version=$(kubectl --context "$context" get deployment demo-webapp -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="VERSION")].value}' 2>/dev/null || echo "unknown")
        echo -e "${CYAN}Application version: $app_version${NC}"
        
        echo ""
    done
    
    if [[ "$all_healthy" == "true" ]]; then
        echo -e "${GREEN}${CHECK} All applications are healthy${NC}"
        return 0
    else
        echo -e "${RED}${CROSS} Some applications have issues${NC}"
        return 1
    fi
}

validate_syncers() {
    echo -e "${BOLD}${BLUE}🔗 TMC Syncer Validation${NC}"
    echo ""
    
    local all_healthy=true
    
    # Validate TMC syncers in each environment cluster
    local environments=("$DEV_CONTEXT:Development" "$STAGING_CONTEXT:Staging" "$PROD_CONTEXT:Production")
    
    for env_info in "${environments[@]}"; do
        IFS=':' read -r context env_name <<< "$env_info"
        
        echo -e "${CYAN}$env_name Syncer:${NC}"
        validate_deployment "$context" "kcp-syncer" "default" 1 || all_healthy=false
        
        # Check syncer configuration
        if kubectl --context "$context" get deployment kcp-syncer &>/dev/null; then
            local gitops_enabled=$(kubectl --context "$context" get deployment kcp-syncer -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="GITOPS_INTEGRATION")].value}' 2>/dev/null || echo "unknown")
            if [[ "$gitops_enabled" == "enabled" ]]; then
                echo -e "${GREEN}${CHECK} GitOps integration: enabled${NC}"
            else
                echo -e "${YELLOW}${WARNING} GitOps integration: $gitops_enabled${NC}"
                all_healthy=false
            fi
        fi
        
        echo ""
    done
    
    if [[ "$all_healthy" == "true" ]]; then
        echo -e "${GREEN}${CHECK} TMC syncers are healthy${NC}"
        return 0
    else
        echo -e "${RED}${CROSS} TMC syncers have issues${NC}"
        return 1
    fi
}

test_gitops_workflow() {
    echo -e "${BOLD}${BLUE}⚡ GitOps Workflow Test${NC}"
    echo ""
    
    local test_passed=true
    
    echo -e "${CYAN}Testing GitOps workflow components...${NC}"
    
    # Check if git repositories exist
    local demo_dir="$(dirname "$0")"
    local git_repos_dir="$demo_dir/git-repos"
    
    if [[ -d "$git_repos_dir/demo-app" ]]; then
        echo -e "${GREEN}${CHECK} Application git repository exists${NC}"
        
        # Check if it's a valid git repo
        if git -C "$git_repos_dir/demo-app" status &>/dev/null; then
            echo -e "${GREEN}${CHECK} Application repository is valid git repo${NC}"
            
            # Check for environment configurations
            local env_configs=("dev" "staging" "prod")
            for env in "${env_configs[@]}"; do
                if [[ -f "$git_repos_dir/demo-app/environments/$env/webapp.yaml" ]]; then
                    echo -e "${GREEN}${CHECK} $env environment configuration exists${NC}"
                else
                    echo -e "${RED}${CROSS} $env environment configuration missing${NC}"
                    test_passed=false
                fi
            done
        else
            echo -e "${RED}${CROSS} Application repository is not a valid git repo${NC}"
            test_passed=false
        fi
    else
        echo -e "${RED}${CROSS} Application git repository not found${NC}"
        test_passed=false
    fi
    
    if [[ -d "$git_repos_dir/argocd-config" ]]; then
        echo -e "${GREEN}${CHECK} ArgoCD configuration repository exists${NC}"
        
        # Check ArgoCD applications
        local argocd_apps=("dev-app.yaml" "staging-app.yaml" "prod-app.yaml")
        for app in "${argocd_apps[@]}"; do
            if [[ -f "$git_repos_dir/argocd-config/applications/$app" ]]; then
                echo -e "${GREEN}${CHECK} ArgoCD application $app exists${NC}"
            else
                echo -e "${RED}${CROSS} ArgoCD application $app missing${NC}"
                test_passed=false
            fi
        done
    else
        echo -e "${RED}${CROSS} ArgoCD configuration repository not found${NC}"
        test_passed=false
    fi
    
    echo ""
    echo -e "${CYAN}Testing workflow scripts...${NC}"
    
    # Check if workflow scripts exist and are executable
    local scripts_dir="$demo_dir/scripts"
    local workflow_scripts=("show-app-status.sh" "monitor-gitops.sh" "simulate-code-change.sh")
    
    for script in "${workflow_scripts[@]}"; do
        if [[ -x "$scripts_dir/$script" ]]; then
            echo -e "${GREEN}${CHECK} $script is executable${NC}"
        else
            echo -e "${RED}${CROSS} $script is missing or not executable${NC}"
            test_passed=false
        fi
    done
    
    echo ""
    
    if [[ "$test_passed" == "true" ]]; then
        echo -e "${GREEN}${CHECK} GitOps workflow tests passed${NC}"
        return 0
    else
        echo -e "${RED}${CROSS} GitOps workflow tests failed${NC}"
        return 1
    fi
}

run_all_validations() {
    echo -e "${BOLD}${ROCKET} Running Complete Demo Validation${NC}"
    echo ""
    
    local all_passed=true
    
    validate_clusters || all_passed=false
    echo ""
    
    validate_argocd || all_passed=false  
    echo ""
    
    validate_applications || all_passed=false
    echo ""
    
    validate_syncers || all_passed=false
    echo ""
    
    test_gitops_workflow || all_passed=false
    echo ""
    
    # Summary
    echo -e "${BOLD}${BLUE}📊 Validation Summary${NC}"
    echo "=================================="
    
    if [[ "$all_passed" == "true" ]]; then
        echo -e "${GREEN}${CHECK} All validation tests PASSED${NC}"
        echo ""
        echo -e "${CYAN}Your TMC GitOps Integration demo is fully functional!${NC}"
        echo ""
        echo -e "${YELLOW}Next steps:${NC}"
        echo -e "  • Monitor GitOps workflow: ${BOLD}./scripts/monitor-gitops.sh${NC}"
        echo -e "  • Check application status: ${BOLD}./scripts/show-app-status.sh${NC}"
        echo -e "  • Simulate code changes: ${BOLD}./scripts/simulate-code-change.sh v1.2.0${NC}"
        echo ""
        return 0
    else
        echo -e "${RED}${CROSS} Some validation tests FAILED${NC}"
        echo ""
        echo -e "${YELLOW}Troubleshooting steps:${NC}"
        echo -e "  • Check application status: ${BOLD}./scripts/show-app-status.sh${NC}"
        echo -e "  • Review demo logs in the logs/ directory"
        echo -e "  • Try running the demo again: ${BOLD}./run-demo.sh${NC}"
        echo ""
        return 1
    fi
}

# Main execution
main() {
    local check_clusters=false
    local check_argocd=false
    local check_apps=false
    local check_syncers=false
    local check_gitops=false
    local check_all=true
    
    # Parse arguments
    while [[ $# -gt 0 ]]; do
        case $1 in
            --check-clusters)
                check_clusters=true
                check_all=false
                shift
                ;;
            --check-argocd)
                check_argocd=true
                check_all=false
                shift
                ;;
            --check-apps)
                check_apps=true
                check_all=false
                shift
                ;;
            --check-syncers)
                check_syncers=true
                check_all=false
                shift
                ;;
            --check-gitops)
                check_gitops=true
                check_all=false
                shift
                ;;
            --check-all)
                check_all=true
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
    
    if [[ "$check_all" == "true" ]]; then
        run_all_validations
    else
        local validation_passed=true
        
        [[ "$check_clusters" == "true" ]] && { validate_clusters || validation_passed=false; echo ""; }
        [[ "$check_argocd" == "true" ]] && { validate_argocd || validation_passed=false; echo ""; }
        [[ "$check_apps" == "true" ]] && { validate_applications || validation_passed=false; echo ""; }
        [[ "$check_syncers" == "true" ]] && { validate_syncers || validation_passed=false; echo ""; }
        [[ "$check_gitops" == "true" ]] && { test_gitops_workflow || validation_passed=false; echo ""; }
        
        if [[ "$validation_passed" == "true" ]]; then
            echo -e "${GREEN}${CHECK} Selected validation tests passed${NC}"
            exit 0
        else
            echo -e "${RED}${CROSS} Some validation tests failed${NC}"
            exit 1
        fi
    fi
}

# Run the script
main "$@"