#!/bin/bash

# TMC Cross-Cluster Controller Monitoring Script
# Visualizes TaskQueue status and controller operations across multiple clusters

set -e

# Demo configuration
DEMO_DIR="$(dirname "$0")/.."
KUBECONFIG_DIR="$DEMO_DIR/kubeconfigs"

# Cluster contexts
KCP_CONTEXT="kind-controller-kcp"
EAST_CONTEXT="kind-controller-east" 
WEST_CONTEXT="kind-controller-west"

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
PROCESSING="🔄"
CLOCK="⏱️"
ROCKET="🚀"
MONITOR="📊"
CONTROLLER="🎮"
CLUSTER="🔗"
SYNC="🔄"

# Clear screen and position cursor
clear_screen() {
    clear
    printf '\033[H'
}

# Print header
print_header() {
    echo -e "${BOLD}${PURPLE}================================================${NC}"
    echo -e "${BOLD}${PURPLE}${MONITOR} TMC Cross-Cluster Controller Monitor${NC}"
    echo -e "${BOLD}${PURPLE}================================================${NC}"
    echo -e "${CYAN}Real-time TaskQueue processing across multiple clusters${NC}"
    echo -e "${CYAN}Controller on West cluster processing tasks from ALL clusters${NC}"
    echo ""
}

# Check if demo is running
check_demo_status() {
    local kcp_running=false
    local east_running=false
    local west_running=false
    local controller_running=false

    # Check KCP cluster
    if kubectl --context "$KCP_CONTEXT" get nodes &>/dev/null; then
        kcp_running=true
    fi

    # Check East cluster
    if kubectl --context "$EAST_CONTEXT" get nodes &>/dev/null; then
        east_running=true
    fi

    # Check West cluster  
    if kubectl --context "$WEST_CONTEXT" get nodes &>/dev/null; then
        west_running=true
    fi

    # Check if controller is running
    if kubectl --context "$WEST_CONTEXT" get deployment taskqueue-controller &>/dev/null; then
        if [[ $(kubectl --context "$WEST_CONTEXT" get deployment taskqueue-controller -o jsonpath='{.status.readyReplicas}') -gt 0 ]]; then
            controller_running=true
        fi
    fi

    if [[ "$kcp_running" == false ]] || [[ "$east_running" == false ]] || [[ "$west_running" == false ]]; then
        echo -e "${RED}${CROSS} Demo clusters not running. Please run the demo first:${NC}"
        echo -e "${YELLOW}  cd /mnt/scratch/workspace/kcp/demos/cross-cluster-controller${NC}"
        echo -e "${YELLOW}  ./run-demo.sh${NC}"
        exit 1
    fi

    if [[ "$controller_running" == false ]]; then
        echo -e "${YELLOW}⚠️  TaskQueue controller not ready yet. Starting monitoring anyway...${NC}"
        echo ""
    fi
}

# Get TaskQueue status from a cluster
get_taskqueue_status() {
    local context=$1
    local cluster_name=$2
    
    # Get TaskQueues with jsonpath to extract key information
    kubectl --context "$context" get taskqueues -o jsonpath='{range .items[*]}{.metadata.name}{"|"}{.status.phase}{"|"}{.status.completedTasks}{"|"}{.status.totalTasks}{"|"}{.status.processingCluster}{"|"}{.metadata.labels.origin-cluster}{"|"}{.status.lastProcessedTime}{"\n"}{end}' 2>/dev/null || echo ""
}

# Format TaskQueue status for display
format_taskqueue_display() {
    local name=$1
    local phase=$2
    local completed=$3
    local total=$4
    local processing_cluster=$5
    local origin_cluster=$6
    local last_processed=$7
    local current_cluster=$8

    # Default values
    [[ -z "$phase" ]] && phase="Unknown"
    [[ -z "$completed" ]] && completed="0"
    [[ -z "$total" ]] && total="0"
    [[ -z "$processing_cluster" ]] && processing_cluster="none"
    [[ -z "$origin_cluster" ]] && origin_cluster="unknown"

    # Status icon based on phase
    local status_icon="❓"
    local status_color="$NC"
    case "$phase" in
        "Pending")
            status_icon="⏳"
            status_color="$YELLOW"
            ;;
        "Processing"|"Running")
            status_icon="$PROCESSING"
            status_color="$BLUE"
            ;;
        "Completed"|"Complete")
            status_icon="$CHECK"
            status_color="$GREEN"
            ;;
        "Failed"|"Error")
            status_icon="$CROSS"
            status_color="$RED"
            ;;
    esac

    # Origin cluster indicator
    local origin_indicator=""
    if [[ "$origin_cluster" == "$current_cluster" ]]; then
        origin_indicator="${GREEN}[LOCAL]${NC}"
    else
        origin_indicator="${CYAN}[from $origin_cluster]${NC}"
    fi

    # Controller indicator  
    local controller_indicator=""
    if [[ "$processing_cluster" == "west-controller" ]]; then
        controller_indicator="${PURPLE}${CONTROLLER} west${NC}"
    else
        controller_indicator="${YELLOW}none${NC}"
    fi

    printf "%-20s %s %-12s %s %3s/%-3s %s %-15s %s\n" \
        "$name" \
        "${status_color}${status_icon}${NC}" \
        "${status_color}${phase}${NC}" \
        "$origin_indicator" \
        "$completed" "$total" \
        "$controller_indicator" \
        "$(echo "$last_processed" | cut -c1-15 2>/dev/null || echo "never")"
}

# Display cluster status
print_cluster_status() {
    echo -e "${BOLD}${CLUSTER} Cluster Status${NC}"
    echo "┌─────────────────┬─────────────┬─────────────┬─────────────────┐"
    echo "│ Cluster         │ Status      │ Nodes       │ Controller      │"
    echo "├─────────────────┼─────────────┼─────────────┼─────────────────┤"
    
    # KCP Status
    local kcp_nodes=$(kubectl --context "$KCP_CONTEXT" get nodes --no-headers 2>/dev/null | wc -l)
    echo -e "│ KCP Host        │ ${GREEN}${CHECK} Running${NC}  │ $kcp_nodes nodes     │ ${YELLOW}None${NC}            │"
    
    # East Status
    local east_nodes=$(kubectl --context "$EAST_CONTEXT" get nodes --no-headers 2>/dev/null | wc -l)
    echo -e "│ East Cluster    │ ${GREEN}${CHECK} Running${NC}  │ $east_nodes nodes     │ ${YELLOW}None${NC}            │"
    
    # West Status with Controller
    local west_nodes=$(kubectl --context "$WEST_CONTEXT" get nodes --no-headers 2>/dev/null | wc -l)
    local controller_status="${RED}${CROSS} Down${NC}       "
    if kubectl --context "$WEST_CONTEXT" get deployment taskqueue-controller &>/dev/null; then
        local ready_replicas=$(kubectl --context "$WEST_CONTEXT" get deployment taskqueue-controller -o jsonpath='{.status.readyReplicas}' 2>/dev/null || echo "0")
        if [[ "$ready_replicas" -gt 0 ]]; then
            controller_status="${GREEN}${CHECK} Active${NC}      "
        fi
    fi
    echo -e "│ West Cluster    │ ${GREEN}${CHECK} Running${NC}  │ $west_nodes nodes     │ $controller_status│"
    
    echo "└─────────────────┴─────────────┴─────────────┴─────────────────┘"
    echo ""
}

# Display TaskQueue status across all clusters
print_taskqueue_status() {
    echo -e "${BOLD}${ROCKET} TaskQueue Status Across All Clusters${NC}"
    echo "┌────────────────────┬──────────────┬─────────────┬─────────┬─────────────────┬─────────────────┐"
    echo "│ TaskQueue Name     │ Status       │ Origin      │ Tasks   │ Controller      │ Last Update     │"
    echo "├────────────────────┼──────────────┼─────────────┼─────────┼─────────────────┼─────────────────┤"
    
    local found_taskqueues=false
    
    # Get TaskQueues from all clusters
    local clusters=("$KCP_CONTEXT:kcp" "$EAST_CONTEXT:east" "$WEST_CONTEXT:west")
    
    for cluster_info in "${clusters[@]}"; do
        IFS=':' read -r context cluster_name <<< "$cluster_info"
        
        local taskqueue_data
        taskqueue_data=$(get_taskqueue_status "$context" "$cluster_name")
        
        if [[ -n "$taskqueue_data" ]]; then
            while IFS= read -r line; do
                if [[ -n "$line" ]]; then
                    found_taskqueues=true
                    IFS='|' read -r name phase completed total processing_cluster origin_cluster last_processed <<< "$line"
                    echo -n "│ "
                    format_taskqueue_display "$name" "$phase" "$completed" "$total" "$processing_cluster" "$origin_cluster" "$last_processed" "$cluster_name"
                    echo -n " │"
                    echo ""
                fi
            done <<< "$taskqueue_data"
        fi
    done
    
    if [[ "$found_taskqueues" == false ]]; then
        echo -e "│ ${YELLOW}No TaskQueues found. Create some with:${NC}                                             │"
        echo -e "│ ${CYAN}kubectl apply -f manifests/east-taskqueue.yaml${NC}                                   │"
        echo -e "│ ${CYAN}kubectl apply -f manifests/west-taskqueue.yaml${NC}                                   │"
    fi
    
    echo "└────────────────────┴──────────────┴─────────────┴─────────┴─────────────────┴─────────────────┘"
    echo ""
}

# Display TMC synchronization status
print_sync_status() {
    echo -e "${BOLD}${SYNC} TMC Synchronization Status${NC}"
    
    # Check syncer status on each cluster
    local east_syncer_ready=false
    local west_syncer_ready=false
    
    if kubectl --context "$EAST_CONTEXT" get deployment kcp-syncer &>/dev/null; then
        local ready=$(kubectl --context "$EAST_CONTEXT" get deployment kcp-syncer -o jsonpath='{.status.readyReplicas}' 2>/dev/null || echo "0")
        [[ "$ready" -gt 0 ]] && east_syncer_ready=true
    fi
    
    if kubectl --context "$WEST_CONTEXT" get deployment kcp-syncer &>/dev/null; then
        local ready=$(kubectl --context "$WEST_CONTEXT" get deployment kcp-syncer -o jsonpath='{.status.readyReplicas}' 2>/dev/null || echo "0")
        [[ "$ready" -gt 0 ]] && west_syncer_ready=true
    fi
    
    echo -e "East Syncer:  $(if $east_syncer_ready; then echo "${GREEN}${CHECK} Connected${NC}"; else echo "${RED}${CROSS} Disconnected${NC}"; fi)"
    echo -e "West Syncer:  $(if $west_syncer_ready; then echo "${GREEN}${CHECK} Connected${NC}"; else echo "${RED}${CROSS} Disconnected${NC}"; fi)"
    echo ""
}

# Display help text
print_help() {
    echo -e "${BOLD}Controls:${NC}"
    echo -e "  ${CYAN}Ctrl+C${NC}    Stop monitoring"
    echo -e "  ${CYAN}r${NC}         Refresh immediately"
    echo -e "  ${CYAN}h${NC}         Show this help"
    echo -e "  ${CYAN}q${NC}         Quit"
    echo ""
    echo -e "${BOLD}Key Features:${NC}"
    echo -e "  • ${GREEN}Real-time TaskQueue status${NC} across all clusters"
    echo -e "  • ${BLUE}Controller location tracking${NC} (west cluster only)"
    echo -e "  • ${YELLOW}Cross-cluster resource visibility${NC}"
    echo -e "  • ${PURPLE}TMC synchronization status${NC}"
    echo ""
}

# Main monitoring loop
monitor_processing() {
    local refresh_interval=5
    local show_help=false
    
    # Trap Ctrl+C for clean exit
    trap 'echo -e "\n${YELLOW}Monitoring stopped.${NC}"; exit 0' INT
    
    while true; do
        clear_screen
        print_header
        
        if [[ "$show_help" == true ]]; then
            print_help
            show_help=false
        fi
        
        print_cluster_status
        print_taskqueue_status  
        print_sync_status
        
        echo -e "${CYAN}${CLOCK} Updates every ${refresh_interval}s • Press 'h' for help • Ctrl+C to stop${NC}"
        echo -e "${YELLOW}💡 Tip: Create TaskQueues on different clusters to see cross-cluster processing!${NC}"
        
        # Wait for input or timeout
        if read -t $refresh_interval -n 1 key 2>/dev/null; then
            case "$key" in
                'r'|'R')
                    continue  # Refresh immediately
                    ;;
                'h'|'H')
                    show_help=true
                    continue
                    ;;
                'q'|'Q')
                    echo -e "\n${YELLOW}Monitoring stopped.${NC}"
                    exit 0
                    ;;
            esac
        fi
    done
}

# Script entry point
main() {
    if [[ "${1:-}" == "--help" ]] || [[ "${1:-}" == "-h" ]]; then
        echo "TMC Cross-Cluster Controller Monitor"
        echo ""
        echo "This script provides real-time monitoring of TaskQueue processing"
        echo "across multiple clusters, showing how a controller on the west"
        echo "cluster processes CustomResources from all clusters."
        echo ""
        echo "Usage: $0 [options]"
        echo ""
        echo "Options:"
        echo "  -h, --help    Show this help message"
        echo "  --check       Check demo status and exit"
        echo ""
        echo "Prerequisites:"
        echo "  - Cross-cluster controller demo must be running"
        echo "  - TaskQueue CRD must be installed on all clusters"
        echo "  - TaskQueue controller must be deployed on west cluster"
        echo ""
        exit 0
    fi
    
    if [[ "${1:-}" == "--check" ]]; then
        echo "Checking demo status..."
        check_demo_status
        echo -e "${GREEN}${CHECK} Demo is ready for monitoring${NC}"
        exit 0
    fi
    
    echo -e "${CYAN}Starting TMC Cross-Cluster Controller Monitor...${NC}"
    echo -e "${YELLOW}Checking demo status...${NC}"
    
    check_demo_status
    
    echo -e "${GREEN}${CHECK} Demo clusters detected${NC}"
    echo -e "${CYAN}Press any key to start monitoring...${NC}"
    read -n 1 -s
    
    monitor_processing
}

# Run the script
main "$@"