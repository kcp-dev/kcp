#!/bin/bash

# TMC Disaster Recovery - Status Display Script
# Shows current status of all regions and applications

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
CLUSTER="🔗"

print_header() {
    echo -e "${BOLD}${PURPLE}================================================${NC}"
    echo -e "${BOLD}${PURPLE}🌍 TMC Disaster Recovery Status${NC}"
    echo -e "${BOLD}${PURPLE}================================================${NC}"
    echo -e "${CYAN}$(date)${NC}"
    echo ""
}

check_cluster_health() {
    local context=$1
    local cluster_name=$2
    local region=$3
    
    echo -e "${BOLD}${BLUE}$cluster_name Cluster ($region)${NC}"
    echo -e "${CYAN}Context: $context${NC}"
    
    # Check if cluster is accessible
    if ! kubectl --context "$context" get nodes &>/dev/null; then
        echo -e "${RED}${CROSS} Cluster: UNREACHABLE${NC}"
        echo ""
        return 1
    fi
    
    # Get node status
    local nodes_ready=$(kubectl --context "$context" get nodes --no-headers 2>/dev/null | grep -c "Ready" || echo "0")
    local total_nodes=$(kubectl --context "$context" get nodes --no-headers 2>/dev/null | wc -l || echo "0")
    
    if [[ $nodes_ready -eq $total_nodes ]] && [[ $total_nodes -gt 0 ]]; then
        echo -e "${GREEN}${CHECK} Cluster: HEALTHY ($nodes_ready/$total_nodes nodes ready)${NC}"
    else
        echo -e "${RED}${CROSS} Cluster: UNHEALTHY ($nodes_ready/$total_nodes nodes ready)${NC}"
    fi
    
    # Check deployments
    echo -e "${CYAN}Deployments:${NC}"
    kubectl --context "$context" get deployments --no-headers 2>/dev/null | while read -r name ready uptodate available age; do
        if [[ "$ready" == "$available" ]] && [[ "$available" != "0" ]]; then
            echo -e "  ${GREEN}${CHECK} $name: $ready ready${NC}"
        else
            echo -e "  ${RED}${CROSS} $name: $ready ready (expected $available)${NC}"
        fi
    done || echo -e "  ${YELLOW}${WARNING} No deployments found${NC}"
    
    # Check services
    echo -e "${CYAN}Services:${NC}"
    kubectl --context "$context" get services --no-headers 2>/dev/null | grep -v "kubernetes" | while read -r name type cluster_ip external_ip port age; do
        echo -e "  ${GREEN}${CHECK} $name ($type): $cluster_ip${NC}"
    done || echo -e "  ${YELLOW}${WARNING} No services found${NC}"
    
    echo ""
}

show_traffic_distribution() {
    echo -e "${BOLD}${BLUE}Traffic Distribution${NC}"
    
    # Simulate traffic distribution based on deployment status
    local east_healthy=false
    local west_healthy=false
    
    # Check East region health
    if kubectl --context "$EAST_CONTEXT" get deployment webapp-east &>/dev/null; then
        local east_replicas=$(kubectl --context "$EAST_CONTEXT" get deployment webapp-east -o jsonpath='{.status.readyReplicas}' 2>/dev/null || echo "0")
        if [[ "$east_replicas" -gt 0 ]]; then
            east_healthy=true
        fi
    fi
    
    # Check West region health
    if kubectl --context "$WEST_CONTEXT" get deployment webapp-west &>/dev/null; then
        local west_replicas=$(kubectl --context "$WEST_CONTEXT" get deployment webapp-west -o jsonpath='{.status.readyReplicas}' 2>/dev/null || echo "0")
        if [[ "$west_replicas" -gt 0 ]]; then
            west_healthy=true
        fi
    fi
    
    # Show traffic distribution
    if [[ "$east_healthy" == true ]] && [[ "$west_healthy" == true ]]; then
        echo -e "${GREEN}${CHECK} Load Balancing: Active-Active${NC}"
        echo -e "  • East Region (us-east-1): 50% traffic"
        echo -e "  • West Region (us-west-2): 50% traffic"
    elif [[ "$east_healthy" == true ]] && [[ "$west_healthy" == false ]]; then
        echo -e "${YELLOW}${WARNING} Failover Mode: East Only${NC}"
        echo -e "  • East Region (us-east-1): 100% traffic"
        echo -e "  • West Region (us-west-2): 0% traffic (FAILED)"
    elif [[ "$east_healthy" == false ]] && [[ "$west_healthy" == true ]]; then
        echo -e "${YELLOW}${WARNING} Failover Mode: West Only${NC}"
        echo -e "  • East Region (us-east-1): 0% traffic (FAILED)"
        echo -e "  • West Region (us-west-2): 100% traffic"
    else
        echo -e "${RED}${CROSS} System Down: No healthy regions${NC}"
        echo -e "  • East Region (us-east-1): 0% traffic (FAILED)"
        echo -e "  • West Region (us-west-2): 0% traffic (FAILED)"
    fi
    
    echo ""
}

show_tmc_components() {
    echo -e "${BOLD}${BLUE}TMC Components${NC}"
    
    # Check KCP global components
    echo -e "${CYAN}Global Control Plane (KCP):${NC}"
    if kubectl --context "$KCP_CONTEXT" get deployment global-loadbalancer &>/dev/null; then
        local lb_status=$(kubectl --context "$KCP_CONTEXT" get deployment global-loadbalancer -o jsonpath='{.status.readyReplicas}' 2>/dev/null || echo "0")
        if [[ "$lb_status" -gt 0 ]]; then
            echo -e "  ${GREEN}${CHECK} Global Load Balancer: Running${NC}"
        else
            echo -e "  ${RED}${CROSS} Global Load Balancer: Not Ready${NC}"
        fi
    else
        echo -e "  ${YELLOW}${WARNING} Global Load Balancer: Not Deployed${NC}"
    fi
    
    if kubectl --context "$KCP_CONTEXT" get deployment failover-controller &>/dev/null; then
        local fc_status=$(kubectl --context "$KCP_CONTEXT" get deployment failover-controller -o jsonpath='{.status.readyReplicas}' 2>/dev/null || echo "0")
        if [[ "$fc_status" -gt 0 ]]; then
            echo -e "  ${GREEN}${CHECK} Failover Controller: Running${NC}"
        else
            echo -e "  ${RED}${CROSS} Failover Controller: Not Ready${NC}"
        fi
    else
        echo -e "  ${YELLOW}${WARNING} Failover Controller: Not Deployed${NC}"
    fi
    
    # Check regional syncers
    echo -e "${CYAN}Regional Syncers:${NC}"
    for context_info in "$EAST_CONTEXT:East" "$WEST_CONTEXT:West"; do
        IFS=':' read -r context region <<< "$context_info"
        
        if kubectl --context "$context" get deployment kcp-syncer &>/dev/null; then
            local syncer_status=$(kubectl --context "$context" get deployment kcp-syncer -o jsonpath='{.status.readyReplicas}' 2>/dev/null || echo "0")
            if [[ "$syncer_status" -gt 0 ]]; then
                echo -e "  ${GREEN}${CHECK} $region Syncer: Connected${NC}"
            else
                echo -e "  ${RED}${CROSS} $region Syncer: Disconnected${NC}"
            fi
        else
            echo -e "  ${YELLOW}${WARNING} $region Syncer: Not Deployed${NC}"
        fi
    done
    
    echo ""
}

# Main execution
main() {
    print_header
    
    # Check each cluster
    check_cluster_health "$KCP_CONTEXT" "KCP Host" "Global"
    check_cluster_health "$EAST_CONTEXT" "East" "us-east-1"
    check_cluster_health "$WEST_CONTEXT" "West" "us-west-2"
    
    # Show traffic distribution
    show_traffic_distribution
    
    # Show TMC components
    show_tmc_components
    
    echo -e "${BOLD}${CYAN}For real-time monitoring, run:${NC}"
    echo -e "${YELLOW}  ./scripts/monitor-failover.sh${NC}"
    echo ""
}

# Run the script
main "$@"