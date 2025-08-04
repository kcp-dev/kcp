#!/bin/bash

# TMC Disaster Recovery - Simulate Regional Recovery
# Simulates recovery of a failed region to demonstrate automatic rebalancing

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
CHECK="✅"
ROCKET="🚀"
HEART="💓"

print_usage() {
    echo "TMC Disaster Recovery - Simulate Regional Recovery"
    echo ""
    echo "Usage: $0 <region>"
    echo ""
    echo "Regions:"
    echo "  east    Recover East region (us-east-1)"
    echo "  west    Recover West region (us-west-2)"
    echo ""
    echo "This script will scale up applications and health monitors"
    echo "in the specified region to simulate recovery from failure."
    echo ""
    echo "To simulate a failure, use: ./simulate-failure.sh <region>"
    echo ""
}

recover_east_region() {
    echo -e "${BOLD}${GREEN}${ROCKET} Recovering East Region${NC}"
    echo -e "${YELLOW}Region: us-east-1${NC}"
    echo -e "${YELLOW}Cluster: $EAST_CONTEXT${NC}"
    echo ""
    
    echo -e "${BLUE}Scaling up East region components...${NC}"
    
    # Scale up webapp
    echo -e "${CYAN}Starting webapp-east deployment...${NC}"
    if kubectl --context "$EAST_CONTEXT" scale deployment/webapp-east --replicas=2; then
        echo -e "${GREEN}✓ webapp-east scaled to 2 replicas${NC}"
    else
        echo -e "${RED}❌ Failed to scale webapp-east${NC}"
        return 1
    fi
    
    # Scale up health monitor
    echo -e "${CYAN}Starting health-monitor-east deployment...${NC}"
    if kubectl --context "$EAST_CONTEXT" scale deployment/health-monitor-east --replicas=1; then
        echo -e "${GREEN}✓ health-monitor-east scaled to 1 replica${NC}"
    else
        echo -e "${RED}❌ Failed to scale health-monitor-east${NC}"
        return 1
    fi
    
    # Scale up syncer if it was scaled down
    echo -e "${CYAN}Starting kcp-syncer (reconnecting to global control plane)...${NC}"
    if kubectl --context "$EAST_CONTEXT" scale deployment/kcp-syncer --replicas=1 2>/dev/null; then
        echo -e "${GREEN}✓ kcp-syncer scaled to 1 replica (reconnected)${NC}"
    else
        echo -e "${YELLOW}⚠️ kcp-syncer may already be running${NC}"
    fi
    
    echo ""
    echo -e "${BLUE}Waiting for deployments to be ready...${NC}"
    
    # Wait for webapp to be ready
    echo -e "${CYAN}Waiting for webapp-east to be ready...${NC}"
    if kubectl --context "$EAST_CONTEXT" wait --for=condition=available --timeout=300s deployment/webapp-east; then
        echo -e "${GREEN}✓ webapp-east is ready${NC}"
    else
        echo -e "${RED}❌ Timeout waiting for webapp-east${NC}"
    fi
    
    # Wait for health monitor to be ready
    echo -e "${CYAN}Waiting for health-monitor-east to be ready...${NC}"
    if kubectl --context "$EAST_CONTEXT" wait --for=condition=available --timeout=300s deployment/health-monitor-east; then
        echo -e "${GREEN}✓ health-monitor-east is ready${NC}"
    else
        echo -e "${RED}❌ Timeout waiting for health-monitor-east${NC}"
    fi
    
    echo ""
    echo -e "${GREEN}${CHECK} East region has RECOVERED${NC}"
    echo -e "${CYAN}Traffic should automatically rebalance between East and West regions${NC}"
    echo ""
    echo -e "${YELLOW}Monitor the recovery with:${NC}"
    echo -e "${BOLD}  ./scripts/monitor-failover.sh${NC}"
    echo ""
}

recover_west_region() {
    echo -e "${BOLD}${GREEN}${ROCKET} Recovering West Region${NC}"
    echo -e "${YELLOW}Region: us-west-2${NC}"
    echo -e "${YELLOW}Cluster: $WEST_CONTEXT${NC}"
    echo ""
    
    echo -e "${BLUE}Scaling up West region components...${NC}"
    
    # Scale up webapp
    echo -e "${CYAN}Starting webapp-west deployment...${NC}"
    if kubectl --context "$WEST_CONTEXT" scale deployment/webapp-west --replicas=2; then
        echo -e "${GREEN}✓ webapp-west scaled to 2 replicas${NC}"
    else
        echo -e "${RED}❌ Failed to scale webapp-west${NC}"
        return 1
    fi
    
    # Scale up health monitor
    echo -e "${CYAN}Starting health-monitor-west deployment...${NC}"
    if kubectl --context "$WEST_CONTEXT" scale deployment/health-monitor-west --replicas=1; then
        echo -e "${GREEN}✓ health-monitor-west scaled to 1 replica${NC}"
    else
        echo -e "${RED}❌ Failed to scale health-monitor-west${NC}"
        return 1
    fi
    
    # Scale up syncer if it was scaled down
    echo -e "${CYAN}Starting kcp-syncer (reconnecting to global control plane)...${NC}"
    if kubectl --context "$WEST_CONTEXT" scale deployment/kcp-syncer --replicas=1 2>/dev/null; then
        echo -e "${GREEN}✓ kcp-syncer scaled to 1 replica (reconnected)${NC}"
    else
        echo -e "${YELLOW}⚠️ kcp-syncer may already be running${NC}"
    fi
    
    echo ""
    echo -e "${BLUE}Waiting for deployments to be ready...${NC}"
    
    # Wait for webapp to be ready
    echo -e "${CYAN}Waiting for webapp-west to be ready...${NC}"
    if kubectl --context "$WEST_CONTEXT" wait --for=condition=available --timeout=300s deployment/webapp-west; then
        echo -e "${GREEN}✓ webapp-west is ready${NC}"
    else
        echo -e "${RED}❌ Timeout waiting for webapp-west${NC}"
    fi
    
    # Wait for health monitor to be ready
    echo -e "${CYAN}Waiting for health-monitor-west to be ready...${NC}"
    if kubectl --context "$WEST_CONTEXT" wait --for=condition=available --timeout=300s deployment/health-monitor-west; then
        echo -e "${GREEN}✓ health-monitor-west is ready${NC}"
    else
        echo -e "${RED}❌ Timeout waiting for health-monitor-west${NC}"
    fi
    
    echo ""
    echo -e "${GREEN}${CHECK} West region has RECOVERED${NC}"
    echo -e "${CYAN}Traffic should automatically rebalance between East and West regions${NC}"
    echo ""
    echo -e "${YELLOW}Monitor the recovery with:${NC}"
    echo -e "${BOLD}  ./scripts/monitor-failover.sh${NC}"
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
            recover_east_region
            ;;
        "west"|"West"|"WEST")
            recover_west_region
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