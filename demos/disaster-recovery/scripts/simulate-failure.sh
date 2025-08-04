#!/bin/bash

# TMC Disaster Recovery - Simulate Regional Failure
# Simulates failure of a specific region to demonstrate failover

set -e

# Cluster contexts
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
CROSS="❌"
WARNING="⚠️"
LIGHTNING="⚡"

print_usage() {
    echo "TMC Disaster Recovery - Simulate Regional Failure"
    echo ""
    echo "Usage: $0 <region>"
    echo ""
    echo "Regions:"
    echo "  east    Simulate failure of East region (us-east-1)"
    echo "  west    Simulate failure of West region (us-west-2)"
    echo ""
    echo "This script will scale down applications and health monitors"
    echo "in the specified region to simulate a regional failure."
    echo ""
    echo "To recover a region, use: ./simulate-recovery.sh <region>"
    echo ""
}

simulate_east_failure() {
    echo -e "${BOLD}${RED}${LIGHTNING} Simulating East Region Failure${NC}"
    echo -e "${YELLOW}Region: us-east-1${NC}"
    echo -e "${YELLOW}Cluster: $EAST_CONTEXT${NC}"
    echo ""
    
    echo -e "${BLUE}Scaling down East region components...${NC}"
    
    # Scale down webapp
    echo -e "${CYAN}Stopping webapp-east deployment...${NC}"
    if kubectl --context "$EAST_CONTEXT" scale deployment/webapp-east --replicas=0; then
        echo -e "${GREEN}✓ webapp-east scaled to 0 replicas${NC}"
    else
        echo -e "${RED}${CROSS} Failed to scale webapp-east${NC}"
    fi
    
    # Scale down health monitor
    echo -e "${CYAN}Stopping health-monitor-east deployment...${NC}"
    if kubectl --context "$EAST_CONTEXT" scale deployment/health-monitor-east --replicas=0; then
        echo -e "${GREEN}✓ health-monitor-east scaled to 0 replicas${NC}"
    else
        echo -e "${RED}${CROSS} Failed to scale health-monitor-east${NC}"
    fi
    
    # Optional: Scale down syncer to simulate complete regional isolation
    echo -e "${CYAN}Optionally stopping kcp-syncer (regional isolation)...${NC}"
    if kubectl --context "$EAST_CONTEXT" scale deployment/kcp-syncer --replicas=0 2>/dev/null; then
        echo -e "${GREEN}✓ kcp-syncer scaled to 0 replicas (complete isolation)${NC}"
    else
        echo -e "${YELLOW}${WARNING} kcp-syncer not found or already stopped${NC}"
    fi
    
    echo ""
    echo -e "${RED}${CROSS} East region is now FAILED${NC}"
    echo -e "${CYAN}Traffic should automatically failover to West region${NC}"
    echo ""
    echo -e "${YELLOW}Monitor the failover with:${NC}"
    echo -e "${BOLD}  ./scripts/monitor-failover.sh${NC}"
    echo ""
    echo -e "${YELLOW}To recover East region:${NC}"
    echo -e "${BOLD}  ./scripts/simulate-recovery.sh east${NC}"
    echo ""
}

simulate_west_failure() {
    echo -e "${BOLD}${RED}${LIGHTNING} Simulating West Region Failure${NC}"
    echo -e "${YELLOW}Region: us-west-2${NC}"
    echo -e "${YELLOW}Cluster: $WEST_CONTEXT${NC}"
    echo ""
    
    echo -e "${BLUE}Scaling down West region components...${NC}"
    
    # Scale down webapp
    echo -e "${CYAN}Stopping webapp-west deployment...${NC}"
    if kubectl --context "$WEST_CONTEXT" scale deployment/webapp-west --replicas=0; then
        echo -e "${GREEN}✓ webapp-west scaled to 0 replicas${NC}"
    else
        echo -e "${RED}${CROSS} Failed to scale webapp-west${NC}"
    fi
    
    # Scale down health monitor
    echo -e "${CYAN}Stopping health-monitor-west deployment...${NC}"
    if kubectl --context "$WEST_CONTEXT" scale deployment/health-monitor-west --replicas=0; then
        echo -e "${GREEN}✓ health-monitor-west scaled to 0 replicas${NC}"
    else
        echo -e "${RED}${CROSS} Failed to scale health-monitor-west${NC}"
    fi
    
    # Optional: Scale down syncer to simulate complete regional isolation
    echo -e "${CYAN}Optionally stopping kcp-syncer (regional isolation)...${NC}"
    if kubectl --context "$WEST_CONTEXT" scale deployment/kcp-syncer --replicas=0 2>/dev/null; then
        echo -e "${GREEN}✓ kcp-syncer scaled to 0 replicas (complete isolation)${NC}"
    else
        echo -e "${YELLOW}${WARNING} kcp-syncer not found or already stopped${NC}"
    fi
    
    echo ""
    echo -e "${RED}${CROSS} West region is now FAILED${NC}"
    echo -e "${CYAN}Traffic should automatically failover to East region${NC}"
    echo ""
    echo -e "${YELLOW}Monitor the failover with:${NC}"
    echo -e "${BOLD}  ./scripts/monitor-failover.sh${NC}"
    echo ""
    echo -e "${YELLOW}To recover West region:${NC}"
    echo -e "${BOLD}  ./scripts/simulate-recovery.sh west${NC}"
    echo ""
}

# Main execution
main() {
    local region="${1:-}"
    
    if [[ -z "$region" ]]; then
        print_usage
        exit 1
    fi
    
    case "$region" in
        "east"|"East"|"EAST")
            simulate_east_failure
            ;;
        "west"|"West"|"WEST")
            simulate_west_failure
            ;;
        "help"|"-h"|"--help")
            print_usage
            exit 0
            ;;
        *)
            echo -e "${RED}Error: Unknown region '$region'${NC}"
            echo ""
            print_usage
            exit 1
            ;;
    esac
}

# Run the script
main "$@"