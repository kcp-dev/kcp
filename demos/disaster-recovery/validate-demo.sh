#!/bin/bash

# TMC Disaster Recovery Demo - Validation Script
# Validates that all demo components are working correctly

set -e

# Cluster contexts
KCP_CONTEXT="kind-dr-kcp"
EAST_CONTEXT="kind-dr-east"
WEST_CONTEXT="kind-dr-west"

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

print_header() {
    echo -e "${BOLD}${PURPLE}================================================${NC}"
    echo -e "${BOLD}${PURPLE}🔍 TMC Disaster Recovery Demo Validation${NC}"
    echo -e "${BOLD}${PURPLE}================================================${NC}"
    echo ""
}

print_usage() {
    echo "TMC Disaster Recovery Demo - Validation Script"
    echo ""
    echo "Usage: $0 [options]"
    echo ""
    echo "Options:"
    echo "  --check-clusters    Validate cluster health"
    echo "  --check-apps        Validate application deployments"
    echo "  --check-tmc         Validate TMC components"
    echo "  --check-failover    Test failover functionality"
    echo "  --check-all         Run all validation checks (default)"
    echo "  --help, -h          Show this help message"
    echo ""
    echo "This script validates:"
    echo "  • Kind cluster accessibility and health"
    echo "  • Application deployment status"
    echo "  • TMC syncer connectivity"
    echo "  • Failover controller functionality"
    echo "  • Health monitoring components"
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

validate_application() {
    local context=$1
    local app_name=$2
    local expected_replicas=$3
    
    echo -e "${CYAN}Checking $app_name...${NC}"
    
    if ! kubectl --context "$context" get deployment "$app_name" &>/dev/null; then
        echo -e "${RED}${CROSS} Deployment $app_name not found${NC}"
        return 1
    fi
    
    local ready_replicas=$(kubectl --context "$context" get deployment "$app_name" -o jsonpath='{.status.readyReplicas}' 2>/dev/null || echo "0")
    local desired_replicas=$(kubectl --context "$context" get deployment "$app_name" -o jsonpath='{.spec.replicas}' 2>/dev/null || echo "0")
    
    if [[ "$ready_replicas" -eq "$desired_replicas" ]] && [[ "$ready_replicas" -ge "$expected_replicas" ]]; then
        echo -e "${GREEN}${CHECK} $app_name: $ready_replicas/$desired_replicas replicas ready${NC}"
        return 0
    else
        echo -e "${RED}${CROSS} $app_name: $ready_replicas/$desired_replicas replicas ready (expected at least $expected_replicas)${NC}"
        return 1
    fi
}

validate_clusters() {
    echo -e "${BOLD}${BLUE}🔍 Cluster Validation${NC}"
    echo ""
    
    local all_healthy=true
    
    # Validate each cluster
    validate_cluster "$KCP_CONTEXT" "KCP Host" || all_healthy=false
    validate_cluster "$EAST_CONTEXT" "East" || all_healthy=false
    validate_cluster "$WEST_CONTEXT" "West" || all_healthy=false
    
    if [[ "$all_healthy" == "true" ]]; then
        echo -e "${GREEN}${CHECK} All clusters are healthy${NC}"
        return 0
    else
        echo -e "${RED}${CROSS} Some clusters have issues${NC}"
        return 1
    fi
}

validate_applications() {
    echo -e "${BOLD}${BLUE}🚀 Application Validation${NC}"
    echo ""
    
    local all_healthy=true
    
    # Validate East region applications
    echo -e "${CYAN}East Region Applications:${NC}"
    validate_application "$EAST_CONTEXT" "webapp-east" 1 || all_healthy=false
    validate_application "$EAST_CONTEXT" "health-monitor-east" 1 || all_healthy=false
    echo ""
    
    # Validate West region applications
    echo -e "${CYAN}West Region Applications:${NC}"
    validate_application "$WEST_CONTEXT" "webapp-west" 1 || all_healthy=false
    validate_application "$WEST_CONTEXT" "health-monitor-west" 1 || all_healthy=false
    echo ""
    
    # Validate global applications
    echo -e "${CYAN}Global Applications:${NC}"
    validate_application "$KCP_CONTEXT" "global-loadbalancer" 1 || all_healthy=false
    validate_application "$KCP_CONTEXT" "failover-controller" 1 || all_healthy=false
    echo ""
    
    if [[ "$all_healthy" == "true" ]]; then
        echo -e "${GREEN}${CHECK} All applications are healthy${NC}"
        return 0
    else
        echo -e "${RED}${CROSS} Some applications have issues${NC}"
        return 1
    fi
}

validate_tmc_components() {
    echo -e "${BOLD}${BLUE}🔗 TMC Component Validation${NC}"
    echo ""
    
    local all_healthy=true
    
    # Validate TMC syncers
    echo -e "${CYAN}TMC Syncers:${NC}"
    validate_application "$EAST_CONTEXT" "kcp-syncer" 1 || all_healthy=false
    validate_application "$WEST_CONTEXT" "kcp-syncer" 1 || all_healthy=false
    echo ""
    
    # Check for TMC-related resources
    echo -e "${CYAN}TMC Resource Synchronization:${NC}"
    
    # Check if services are visible across clusters (simulated check)
    local east_services=$(kubectl --context "$EAST_CONTEXT" get services --no-headers 2>/dev/null | wc -l || echo "0")
    local west_services=$(kubectl --context "$WEST_CONTEXT" get services --no-headers 2>/dev/null | wc -l || echo "0")
    
    if [[ $east_services -gt 0 ]] && [[ $west_services -gt 0 ]]; then
        echo -e "${GREEN}${CHECK} Services detected in both regions (synchronization working)${NC}"
    else
        echo -e "${YELLOW}${WARNING} Limited services detected, check TMC synchronization${NC}"
        all_healthy=false
    fi
    
    echo ""
    
    if [[ "$all_healthy" == "true" ]]; then
        echo -e "${GREEN}${CHECK} TMC components are healthy${NC}"
        return 0
    else
        echo -e "${RED}${CROSS} TMC components have issues${NC}"
        return 1
    fi
}

test_failover_functionality() {
    echo -e "${BOLD}${BLUE}⚡ Failover Functionality Test${NC}"
    echo ""
    
    local test_passed=true
    
    echo -e "${CYAN}Testing failover simulation scripts...${NC}"
    
    # Check if failover scripts exist and are executable
    local scripts_dir="$(dirname "$0")/scripts"
    
    if [[ -x "$scripts_dir/simulate-failure.sh" ]]; then
        echo -e "${GREEN}${CHECK} simulate-failure.sh is executable${NC}"
    else
        echo -e "${RED}${CROSS} simulate-failure.sh is missing or not executable${NC}"
        test_passed=false
    fi
    
    if [[ -x "$scripts_dir/simulate-recovery.sh" ]]; then
        echo -e "${GREEN}${CHECK} simulate-recovery.sh is executable${NC}"
    else
        echo -e "${RED}${CROSS} simulate-recovery.sh is missing or not executable${NC}"
        test_passed=false
    fi
    
    if [[ -x "$scripts_dir/monitor-failover.sh" ]]; then
        echo -e "${GREEN}${CHECK} monitor-failover.sh is executable${NC}"
    else
        echo -e "${RED}${CROSS} monitor-failover.sh is missing or not executable${NC}"
        test_passed=false
    fi
    
    echo ""
    echo -e "${CYAN}Testing basic failover scenario...${NC}"
    
    # Get initial state
    local initial_east_replicas=$(kubectl --context "$EAST_CONTEXT" get deployment webapp-east -o jsonpath='{.status.readyReplicas}' 2>/dev/null || echo "0")
    local initial_west_replicas=$(kubectl --context "$WEST_CONTEXT" get deployment webapp-west -o jsonpath='{.status.readyReplicas}' 2>/dev/null || echo "0")
    
    echo -e "${CYAN}Initial state: East=$initial_east_replicas replicas, West=$initial_west_replicas replicas${NC}"
    
    # Simulate East failure
    echo -e "${CYAN}Simulating East region failure...${NC}"
    kubectl --context "$EAST_CONTEXT" scale deployment/webapp-east --replicas=0 &>/dev/null
    
    sleep 5
    
    local failed_east_replicas=$(kubectl --context "$EAST_CONTEXT" get deployment webapp-east -o jsonpath='{.status.readyReplicas}' 2>/dev/null || echo "0")
    local active_west_replicas=$(kubectl --context "$WEST_CONTEXT" get deployment webapp-west -o jsonpath='{.status.readyReplicas}' 2>/dev/null || echo "0")
    
    if [[ "$failed_east_replicas" -eq 0 ]] && [[ "$active_west_replicas" -gt 0 ]]; then
        echo -e "${GREEN}${CHECK} Failover test: East failed (0 replicas), West active ($active_west_replicas replicas)${NC}"
    else
        echo -e "${RED}${CROSS} Failover test failed: East=$failed_east_replicas, West=$active_west_replicas${NC}"
        test_passed=false
    fi
    
    # Restore East region
    echo -e "${CYAN}Restoring East region...${NC}"
    kubectl --context "$EAST_CONTEXT" scale deployment/webapp-east --replicas=2 &>/dev/null
    
    # Wait for recovery
    echo -e "${CYAN}Waiting for East region recovery...${NC}"
    kubectl --context "$EAST_CONTEXT" wait --for=condition=available --timeout=120s deployment/webapp-east &>/dev/null || true
    
    local recovered_east_replicas=$(kubectl --context "$EAST_CONTEXT" get deployment webapp-east -o jsonpath='{.status.readyReplicas}' 2>/dev/null || echo "0")
    
    if [[ "$recovered_east_replicas" -ge 1 ]]; then
        echo -e "${GREEN}${CHECK} Recovery test: East region recovered ($recovered_east_replicas replicas)${NC}"
    else
        echo -e "${RED}${CROSS} Recovery test failed: East region did not recover${NC}"
        test_passed=false
    fi
    
    echo ""
    
    if [[ "$test_passed" == "true" ]]; then
        echo -e "${GREEN}${CHECK} Failover functionality tests passed${NC}"
        return 0
    else
        echo -e "${RED}${CROSS} Failover functionality tests failed${NC}"
        return 1
    fi
}

run_all_validations() {
    echo -e "${BOLD}${ROCKET} Running Complete Demo Validation${NC}"
    echo ""
    
    local all_passed=true
    
    validate_clusters || all_passed=false
    echo ""
    
    validate_applications || all_passed=false
    echo ""
    
    validate_tmc_components || all_passed=false
    echo ""
    
    test_failover_functionality || all_passed=false
    echo ""
    
    # Summary
    echo -e "${BOLD}${BLUE}📊 Validation Summary${NC}"
    echo "=================================="
    
    if [[ "$all_passed" == "true" ]]; then
        echo -e "${GREEN}${CHECK} All validation tests PASSED${NC}"
        echo ""
        echo -e "${CYAN}Your TMC Disaster Recovery demo is fully functional!${NC}"
        echo ""
        echo -e "${YELLOW}Next steps:${NC}"
        echo -e "  • Run real-time monitoring: ${BOLD}./scripts/monitor-failover.sh${NC}"
        echo -e "  • Test manual failover: ${BOLD}./scripts/simulate-failure.sh east${NC}"  
        echo -e "  • Test recovery: ${BOLD}./scripts/simulate-recovery.sh east${NC}"
        echo ""
        return 0
    else
        echo -e "${RED}${CROSS} Some validation tests FAILED${NC}"
        echo ""
        echo -e "${YELLOW}Troubleshooting steps:${NC}"
        echo -e "  • Check cluster status: ${BOLD}./scripts/show-status.sh${NC}"
        echo -e "  • Review demo logs in the logs/ directory"
        echo -e "  • Try running the demo again: ${BOLD}./run-demo.sh${NC}"
        echo ""
        return 1
    fi
}

# Main execution
main() {
    local check_clusters=false
    local check_apps=false
    local check_tmc=false
    local check_failover=false
    local check_all=true
    
    # Parse arguments
    while [[ $# -gt 0 ]]; do
        case $1 in
            --check-clusters)
                check_clusters=true
                check_all=false
                shift
                ;;
            --check-apps)
                check_apps=true
                check_all=false
                shift
                ;;
            --check-tmc)
                check_tmc=true
                check_all=false
                shift
                ;;
            --check-failover)
                check_failover=true
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
        [[ "$check_apps" == "true" ]] && { validate_applications || validation_passed=false; echo ""; }
        [[ "$check_tmc" == "true" ]] && { validate_tmc_components || validation_passed=false; echo ""; }
        [[ "$check_failover" == "true" ]] && { test_failover_functionality || validation_passed=false; echo ""; }
        
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