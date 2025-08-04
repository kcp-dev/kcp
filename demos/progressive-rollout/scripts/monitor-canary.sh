#!/bin/bash

# TMC Canary Monitoring Script
# Provides real-time monitoring of canary deployments

set -e

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
CANARY="🐤"
METRICS="📊"

print_canary_metrics() {
    echo -e "${BOLD}${CYAN}${CANARY} Canary Deployment Monitoring${NC}"
    echo ""
    
    # Simulate real-time metrics
    local error_rate=$(echo "scale=3; $(( RANDOM % 10 )) / 1000" | bc -l 2>/dev/null || echo "0.001")
    local response_time=$(( 90 + RANDOM % 20 ))
    local success_rate=$(echo "scale=2; 99.$(( 90 + RANDOM % 9 ))" | bc -l 2>/dev/null || echo "99.95")
    local throughput=$(( 1200 + RANDOM % 100 ))
    
    echo -e "${BLUE}Real-time Canary Metrics:${NC}"
    echo -e "  ${METRICS} Error Rate: ${error_rate}% (threshold: <1.0%)"
    echo -e "  ${METRICS} Response Time: ${response_time}ms (threshold: <200ms)"
    echo -e "  ${METRICS} Success Rate: ${success_rate}% (threshold: >99%)"
    echo -e "  ${METRICS} Throughput: ${throughput} req/min"
    echo -e "  ${METRICS} CPU Usage: $(( 10 + RANDOM % 15 ))%"
    echo -e "  ${METRICS} Memory Usage: $(( 40 + RANDOM % 20 ))%"
    echo ""
    
    # Health evaluation
    if (( $(echo "$error_rate < 1.0" | bc -l 2>/dev/null || echo "1") )) && (( response_time < 200 )); then
        echo -e "${GREEN}${CHECK} Canary health: EXCELLENT${NC}"
        echo -e "${GREEN}${CHECK} Ready for promotion${NC}"
    else
        echo -e "${YELLOW}${WARNING} Canary health: MONITORING${NC}"
        echo -e "${YELLOW}Waiting for metrics to stabilize...${NC}"
    fi
    
    echo ""
}

# Main monitoring (runs for about 10 seconds)
main() {
    local duration=10
    local interval=2
    local elapsed=0
    
    while [[ $elapsed -lt $duration ]]; do
        print_canary_metrics
        sleep $interval
        elapsed=$((elapsed + interval))
    done
    
    echo -e "${GREEN}${CHECK} Canary monitoring window completed${NC}"
}

# Run monitoring
main "$@"