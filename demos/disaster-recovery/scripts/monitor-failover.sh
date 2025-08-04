#!/bin/bash

# TMC Disaster Recovery - Real-time Failover Monitor
# Provides live dashboard showing regional health and failover status

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
DIM='\033[2m'
NC='\033[0m' # No Color

# Unicode symbols
CHECK="✅"
CROSS="❌"
WARNING="⚠️"
ROCKET="🚀"
CLUSTER="🔗"
GLOBE="🌍"
HEART="💓"
LIGHTNING="⚡"

# Clear screen function
clear_screen() {
    printf '\033[2J\033[H'
}

# Print header
print_header() {
    echo -e "${BOLD}${PURPLE}===============================================================${NC}"
    echo -e "${BOLD}${PURPLE}${GLOBE} TMC Disaster Recovery Monitor${NC}"
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
    
    if ! kubectl --context "$context" get deployment "$deployment" &>/dev/null; then
        echo "0"
        return
    fi
    
    local ready_replicas=$(kubectl --context "$context" get deployment "$deployment" -o jsonpath='{.status.readyReplicas}' 2>/dev/null || echo "0")
    echo "$ready_replicas"
}

# Show cluster status table
show_cluster_status() {
    echo -e "${BOLD}${BLUE}Regional Status${NC}"
    echo -e "┌─────────────────┬─────────────┬─────────────┬─────────────────┬─────────────────┐"
    echo -e "│ Region          │ Cluster     │ Nodes       │ Application     │ TMC Syncer      │"
    echo -e "├─────────────────┼─────────────┼─────────────┼─────────────────┼─────────────────┤"
    
    # Check each region
    local regions=("Global:$KCP_CONTEXT:kcp:none:global-loadbalancer" "East:$EAST_CONTEXT:us-east-1:webapp-east:kcp-syncer" "West:$WEST_CONTEXT:us-west-2:webapp-west:kcp-syncer")
    
    for region_info in "${regions[@]}"; do
        IFS=':' read -r region_name context region_code app_name syncer_name <<< "$region_info"
        
        # Cluster health
        local cluster_healthy=$(check_cluster_health "$context")
        if [[ "$cluster_healthy" == "true" ]]; then
            local nodes_ready=$(kubectl --context "$context" get nodes --no-headers 2>/dev/null | grep -c "Ready" || echo "0")
            local cluster_status="${GREEN}${CHECK} Running${NC}"
            local node_info="${nodes_ready} nodes"
        else
            local cluster_status="${RED}${CROSS} Down${NC}"
            local node_info="0 nodes"
        fi
        
        # Application health
        if [[ "$app_name" != "none" ]]; then
            local app_replicas=$(check_app_health "$context" "$app_name")
            if [[ "$app_replicas" -gt 0 ]]; then
                local app_status="${GREEN}${CHECK} Active${NC}"
            else
                local app_status="${RED}${CROSS} Failed${NC}"
            fi
        else
            local app_status="${CYAN}N/A${NC}"
        fi
        
        # Syncer health
        if [[ "$syncer_name" != "none" ]]; then
            local syncer_replicas=$(check_app_health "$context" "$syncer_name")
            if [[ "$syncer_replicas" -gt 0 ]]; then
                local syncer_status="${GREEN}${CHECK} Connected${NC}"
            else
                local syncer_status="${RED}${CROSS} Disconnected${NC}"
            fi
        else
            # Check failover controller for KCP
            local fc_replicas=$(check_app_health "$context" "failover-controller")
            if [[ "$fc_replicas" -gt 0 ]]; then
                local syncer_status="${GREEN}${CHECK} Controller${NC}"
            else
                local syncer_status="${RED}${CROSS} No Controller${NC}"
            fi
        fi
        
        printf "│ %-15s │ %-11s │ %-11s │ %-15s │ %-15s │\n" "$region_name" "$region_code" "$node_info" "$app_status" "$syncer_status"
    done
    
    echo -e "└─────────────────┴─────────────┴─────────────┴─────────────────┴─────────────────┘"
    echo ""
}

# Show traffic distribution
show_traffic_distribution() {
    echo -e "${BOLD}${BLUE}Traffic Distribution & Failover Status${NC}"
    
    # Check application health for both regions
    local east_replicas=$(check_app_health "$EAST_CONTEXT" "webapp-east")
    local west_replicas=$(check_app_health "$WEST_CONTEXT" "webapp-west")
    
    # Determine failover status
    if [[ "$east_replicas" -gt 0 ]] && [[ "$west_replicas" -gt 0 ]]; then
        local failover_status="${GREEN}${CHECK} Active-Active Load Balancing${NC}"
        local east_traffic="50%"
        local west_traffic="50%"
        local east_status="${GREEN}HEALTHY${NC}"
        local west_status="${GREEN}HEALTHY${NC}"
    elif [[ "$east_replicas" -gt 0 ]] && [[ "$west_replicas" -eq 0 ]]; then
        local failover_status="${YELLOW}${WARNING} Failover: East Only${NC}"
        local east_traffic="100%"
        local west_traffic="0%"
        local east_status="${GREEN}HEALTHY${NC}"
        local west_status="${RED}FAILED${NC}"
    elif [[ "$east_replicas" -eq 0 ]] && [[ "$west_replicas" -gt 0 ]]; then
        local failover_status="${YELLOW}${WARNING} Failover: West Only${NC}"
        local east_traffic="0%"
        local west_traffic="100%"
        local east_status="${RED}FAILED${NC}"
        local west_status="${GREEN}HEALTHY${NC}"
    else
        local failover_status="${RED}${CROSS} System Down: No Healthy Regions${NC}"
        local east_traffic="0%"
        local west_traffic="0%"
        local east_status="${RED}FAILED${NC}"
        local west_status="${RED}FAILED${NC}"
    fi
    
    echo -e "┌─────────────────┬─────────────┬─────────────┬─────────────────┐"
    echo -e "│ Region          │ Status      │ Traffic     │ Replicas        │"
    echo -e "├─────────────────┼─────────────┼─────────────┼─────────────────┤"
    printf "│ East (us-east-1)│ %-11s │ %-11s │ %d/2 pods       │\n" "$east_status" "$east_traffic" "$east_replicas"
    printf "│ West (us-west-2)│ %-11s │ %-11s │ %d/2 pods       │\n" "$west_status" "$west_traffic" "$west_replicas"
    echo -e "└─────────────────┴─────────────┴─────────────┴─────────────────┘"
    echo ""
    echo -e "${BOLD}Failover Status:${NC} $failover_status"
    echo ""
}

# Show recent events
show_events() {
    echo -e "${BOLD}${BLUE}Recent System Events${NC}"
    echo -e "${DIM}(Last 5 events from each cluster)${NC}"
    echo ""
    
    for context_info in "$KCP_CONTEXT:Global" "$EAST_CONTEXT:East" "$WEST_CONTEXT:West"; do
        IFS=':' read -r context region <<< "$context_info"
        
        echo -e "${CYAN}$region Cluster Events:${NC}"
        if kubectl --context "$context" get events --sort-by='.lastTimestamp' 2>/dev/null | tail -5 | head -4; then
            true
        else
            echo -e "${DIM}  No recent events${NC}"
        fi
        echo ""
    done
}

# Show help information
show_help() {
    echo -e "${BOLD}${BLUE}Interactive Commands${NC}"
    echo -e "${CYAN}Available actions:${NC}"
    echo -e "  • Press 'h' for help"
    echo -e "  • Press 's' to simulate East region failure"
    echo -e "  • Press 'r' to recover East region"
    echo -e "  • Press 'w' to simulate West region failure"
    echo -e "  • Press 'e' to recover West region"
    echo -e "  • Press 'q' or Ctrl+C to quit"
    echo ""
}

# Simulate failure
simulate_failure() {
    local region=$1
    
    case $region in
        "east")
            echo -e "${RED}${LIGHTNING} Simulating East region failure...${NC}"
            kubectl --context "$EAST_CONTEXT" scale deployment/webapp-east --replicas=0 &>/dev/null || true
            kubectl --context "$EAST_CONTEXT" scale deployment/health-monitor-east --replicas=0 &>/dev/null || true
            ;;
        "west")
            echo -e "${RED}${LIGHTNING} Simulating West region failure...${NC}"
            kubectl --context "$WEST_CONTEXT" scale deployment/webapp-west --replicas=0 &>/dev/null || true
            kubectl --context "$WEST_CONTEXT" scale deployment/health-monitor-west --replicas=0 &>/dev/null || true
            ;;
    esac
    sleep 2
}

# Simulate recovery
simulate_recovery() {
    local region=$1
    
    case $region in
        "east")
            echo -e "${GREEN}${ROCKET} Recovering East region...${NC}"
            kubectl --context "$EAST_CONTEXT" scale deployment/webapp-east --replicas=2 &>/dev/null || true
            kubectl --context "$EAST_CONTEXT" scale deployment/health-monitor-east --replicas=1 &>/dev/null || true
            ;;
        "west")
            echo -e "${GREEN}${ROCKET} Recovering West region...${NC}"
            kubectl --context "$WEST_CONTEXT" scale deployment/webapp-west --replicas=2 &>/dev/null || true
            kubectl --context "$WEST_CONTEXT" scale deployment/health-monitor-west --replicas=1 &>/dev/null || true
            ;;
    esac
    sleep 2
}

# Main monitoring loop
monitor_failover() {
    local refresh_interval=5
    local show_help_flag=false
    
    # Set up input handling
    stty -echo -icanon time 0 min 0
    
    # Trap Ctrl+C for clean exit
    trap 'echo -e "\n${YELLOW}Monitoring stopped.${NC}"; stty echo icanon; exit 0' INT
    
    while true; do
        clear_screen
        print_header
        show_cluster_status
        show_traffic_distribution
        
        if [[ "$show_help_flag" == true ]]; then
            show_help
            show_help_flag=false
        else
            show_events
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
                simulate_failure "east"
                ;;
            "r"|"R")
                simulate_recovery "east"
                ;;
            "w"|"W")
                simulate_failure "west"
                ;;
            "e"|"E")
                simulate_recovery "west"
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
    echo -e "${BOLD}${PURPLE}${ROCKET} Starting TMC Disaster Recovery Monitor...${NC}"
    echo ""
    echo -e "${CYAN}This monitor provides real-time visibility into:${NC}"
    echo -e "  • Regional cluster health"
    echo -e "  • Application availability"
    echo -e "  • TMC syncer connectivity"
    echo -e "  • Automatic failover events"
    echo -e "  • Traffic distribution changes"
    echo ""
    echo -e "${YELLOW}Press Enter to start monitoring...${NC}"
    read -r
    
    monitor_failover
}

# Check for help
if [[ "${1:-}" == "help" || "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
    echo "TMC Disaster Recovery Monitor"
    echo ""
    echo "This script provides a real-time dashboard showing:"
    echo "  - Regional health status"
    echo "  - Application availability"
    echo "  - Failover events and traffic distribution"
    echo "  - TMC component connectivity"
    echo ""
    echo "Interactive commands available during monitoring:"
    echo "  h - Show help"
    echo "  s - Simulate East region failure"
    echo "  r - Recover East region"
    echo "  w - Simulate West region failure"
    echo "  e - Recover West region"
    echo "  q - Quit monitoring"
    echo ""
    echo "Usage: $0"
    exit 0
fi

# Run the monitor
main "$@"