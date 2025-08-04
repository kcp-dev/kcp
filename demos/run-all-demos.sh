#!/bin/bash

# TMC Demo Launcher Script
# Master script to run all TMC demos individually or together

set -e

SCRIPT_DIR="$(dirname "$0")"
cd "$SCRIPT_DIR"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
PURPLE='\033[0;35m'
CYAN='\033[0;36m'
NC='\033[0m' # No Color

# Demo configurations
declare -A DEMOS=(
    ["hello-world"]="Basic TMC introduction with simple workload synchronization"
    ["cross-cluster-controller"]="Advanced cross-cluster controller with CRDs and status propagation"
    ["helm-deployment"]="Production-ready Helm chart deployment patterns"
    ["production-setup"]="Enterprise-grade multi-cluster setup with monitoring and HA"
)

# Demo order for sequential execution
DEMO_ORDER=("hello-world" "cross-cluster-controller" "helm-deployment" "production-setup")

print_header() {
    echo -e "\n${PURPLE}================================================"
    echo -e "$1"
    echo -e "================================================${NC}\n"
}

print_success() {
    echo -e "${GREEN}✅ $1${NC}"
}

print_warning() {
    echo -e "${YELLOW}⚠️  $1${NC}"
}

print_error() {
    echo -e "${RED}❌ $1${NC}"
}

print_info() {
    echo -e "${CYAN}ℹ️  $1${NC}"
}

print_step() {
    echo -e "\n${BLUE}🔄 $1${NC}\n"
}

# Function to show usage
usage() {
    echo "TMC Demo Launcher"
    echo ""
    echo "Usage: $0 [options] [demo]"
    echo ""
    echo "Options:"
    echo "  --list         List all available demos"
    echo "  --all          Run all demos sequentially"
    echo "  --skip-cleanup Skip cleanup between demos"
    echo "  --help         Show this help message"
    echo ""
    echo "Available demos:"
    for demo in "${DEMO_ORDER[@]}"; do
        echo "  $demo - ${DEMOS[$demo]}"
    done
    echo ""
    echo "Examples:"
    echo "  $0 --list                    # List all demos"
    echo "  $0 hello-world              # Run hello-world demo"
    echo "  $0 --all                    # Run all demos"
    echo "  $0 --all --skip-cleanup     # Run all demos without cleanup"
}

# Function to list demos
list_demos() {
    print_header "📋 Available TMC Demos"
    
    echo "The following demos are available to run independently:"
    echo ""
    
    for demo in "${DEMO_ORDER[@]}"; do
        echo -e "${CYAN}📁 $demo${NC}"
        echo "   Description: ${DEMOS[$demo]}"
        echo "   Location: ./$demo/"
        echo "   Usage: $0 $demo"
        echo ""
    done
    
    echo "Demo progression (recommended order):"
    echo "1. hello-world - Start here for TMC basics"
    echo "2. cross-cluster-controller - Learn advanced controller patterns"
    echo "3. helm-deployment - Understand production deployment"
    echo "4. production-setup - See enterprise-grade setup"
}

# Function to check demo availability
check_demo_availability() {
    local demo="$1"
    
    if [[ ! -d "$demo" ]]; then
        print_error "Demo directory '$demo' not found"
        return 1
    fi
    
    if [[ ! -f "$demo/run-demo.sh" ]]; then
        print_error "Demo script '$demo/run-demo.sh' not found"
        return 1
    fi
    
    if [[ ! -x "$demo/run-demo.sh" ]]; then
        print_error "Demo script '$demo/run-demo.sh' is not executable"
        return 1
    fi
    
    return 0
}

# Function to check system prerequisites
check_system_prerequisites() {
    print_step "Checking system prerequisites"
    
    local missing_tools=()
    
    # Check required tools
    command -v docker >/dev/null 2>&1 || missing_tools+=("docker")
    command -v kubectl >/dev/null 2>&1 || missing_tools+=("kubectl")
    command -v kind >/dev/null 2>&1 || missing_tools+=("kind")
    
    if [ ${#missing_tools[@]} -ne 0 ]; then
        print_error "Missing required tools: ${missing_tools[*]}"
        print_info "Please install the missing tools and try again."
        return 1
    fi
    
    # Check Docker is running
    if ! docker info >/dev/null 2>&1; then
        print_error "Docker is not running. Please start Docker and try again."
        return 1
    fi
    
    # Check available resources
    local available_memory=$(free -m | awk 'NR==2{printf "%.0f", $7}')
    if [[ $available_memory -lt 4096 ]]; then
        print_warning "Low available memory ($available_memory MB)."
        print_info "Recommended: 8GB+ for multiple demos, 16GB+ for production demo"
    fi
    
    print_success "System prerequisites checked"
    return 0
}

# Function to run a single demo
run_demo() {
    local demo="$1"
    local skip_cleanup="$2"
    
    print_header "🚀 Running $demo Demo"
    
    # Check if demo exists and is executable
    if ! check_demo_availability "$demo"; then
        return 1
    fi
    
    print_info "Starting $demo demo..."
    print_info "Description: ${DEMOS[$demo]}"
    echo ""
    
    # Set cleanup environment variable if requested
    if [[ "$skip_cleanup" == "true" ]]; then
        export DEMO_SKIP_CLEANUP=true
        print_info "Cleanup will be skipped for this demo"
    fi
    
    # Run the demo
    if (cd "$demo" && ./run-demo.sh); then
        print_success "$demo demo completed successfully"
        return 0
    else
        print_error "$demo demo failed"
        return 1
    fi
}

# Function to cleanup demo resources
cleanup_demo() {
    local demo="$1"
    
    if [[ -f "$demo/cleanup.sh" && -x "$demo/cleanup.sh" ]]; then
        print_info "Cleaning up $demo demo resources..."
        if (cd "$demo" && ./cleanup.sh --full); then
            print_success "$demo cleanup completed"
        else
            print_warning "$demo cleanup had issues"
        fi
    else
        print_warning "No cleanup script found for $demo"
    fi
}

# Function to run all demos
run_all_demos() {
    local skip_cleanup="$1"
    
    print_header "🎯 Running All TMC Demos"
    
    echo "This will run all demos in the recommended order:"
    for demo in "${DEMO_ORDER[@]}"; do
        echo "• $demo - ${DEMOS[$demo]}"
    done
    echo ""
    
    if [[ "$skip_cleanup" != "true" ]]; then
        echo "Each demo will be cleaned up before running the next one."
        echo "Use --skip-cleanup to keep resources between demos."
    else
        echo "Resources will be kept between demos (--skip-cleanup enabled)."
    fi
    echo ""
    
    read -p "Do you want to continue? (y/N): " -r
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        echo "Demo execution cancelled."
        return 0
    fi
    
    local failed_demos=()
    local successful_demos=()
    
    for demo in "${DEMO_ORDER[@]}"; do
        echo ""
        print_step "Running demo: $demo"
        
        if run_demo "$demo" "$skip_cleanup"; then
            successful_demos+=("$demo")
            
            # Cleanup between demos unless skipped
            if [[ "$skip_cleanup" != "true" ]]; then
                print_step "Cleaning up $demo before next demo"
                cleanup_demo "$demo"
                
                # Wait a bit for cleanup to complete
                sleep 5
            fi
        else
            failed_demos+=("$demo")
            print_error "Demo $demo failed, continuing with next demo..."
            
            # Always cleanup failed demos to avoid conflicts
            cleanup_demo "$demo"
        fi
    done
    
    # Summary
    print_header "📊 Demo Execution Summary"
    
    if [[ ${#successful_demos[@]} -gt 0 ]]; then
        echo -e "${GREEN}✅ Successful demos:${NC}"
        for demo in "${successful_demos[@]}"; do
            echo "  • $demo"
        done
        echo ""
    fi
    
    if [[ ${#failed_demos[@]} -gt 0 ]]; then
        echo -e "${RED}❌ Failed demos:${NC}"
        for demo in "${failed_demos[@]}"; do
            echo "  • $demo"
        done
        echo ""
    fi
    
    echo "Total demos: ${#DEMO_ORDER[@]}"
    echo "Successful: ${#successful_demos[@]}"
    echo "Failed: ${#failed_demos[@]}"
    
    if [[ ${#failed_demos[@]} -eq 0 ]]; then
        print_success "All demos completed successfully!"
    else
        print_warning "Some demos failed. Check logs for details."
    fi
}

# Function to show demo status
show_demo_status() {
    print_header "📊 Demo Environment Status"
    
    # Check for running clusters
    if command -v kind >/dev/null 2>&1; then
        local clusters=$(kind get clusters 2>/dev/null)
        if [[ -n "$clusters" ]]; then
            echo -e "${YELLOW}⚠️  Found running kind clusters:${NC}"
            echo "$clusters" | while read -r cluster; do
                echo "  • $cluster"
            done
            echo ""
            echo "These may conflict with demo execution."
            echo "Consider cleaning up with: kind delete clusters <cluster-name>"
        else
            print_success "No kind clusters found - clean environment"
        fi
    fi
    
    # Check for Docker resources
    local containers=$(docker ps -a --format "table {{.Names}}" 2>/dev/null | grep -E "(hello|controller|helm|prod)" || true)
    if [[ -n "$containers" ]]; then
        echo -e "${YELLOW}⚠️  Found demo-related containers:${NC}"
        echo "$containers"
        echo ""
    fi
    
    # Check system resources
    local available_memory=$(free -m | awk 'NR==2{printf "%.0f", $7}')
    local available_disk=$(df . | awk 'NR==2 {print $4}')
    
    echo "System resources:"
    echo "• Available Memory: ${available_memory}MB"
    echo "• Available Disk: $(($available_disk / 1024 / 1024))GB"
    
    if [[ $available_memory -lt 4096 ]]; then
        print_warning "Low memory may affect demo performance"
    fi
}

# Main function
main() {
    local demo=""
    local run_all=false
    local skip_cleanup=false
    local list_demos=false
    
    # Parse command line arguments
    while [[ $# -gt 0 ]]; do
        case $1 in
            --list)
                list_demos=true
                shift
                ;;
            --all)
                run_all=true
                shift
                ;;
            --skip-cleanup)
                skip_cleanup=true
                shift
                ;;
            --status)
                show_demo_status
                exit 0
                ;;
            --help)
                usage
                exit 0
                ;;
            -*)
                echo "Unknown option: $1"
                usage
                exit 1
                ;;
            *)
                if [[ -n "$demo" ]]; then
                    echo "Multiple demos specified. Use --all to run all demos."
                    exit 1
                fi
                demo="$1"
                shift
                ;;
        esac
    done
    
    # Show header
    print_header "🎯 TMC Demo Launcher"
    
    # Handle list request
    if [[ "$list_demos" == "true" ]]; then
        list_demos
        exit 0
    fi
    
    # Check system prerequisites
    if ! check_system_prerequisites; then
        exit 1
    fi
    
    # Handle run all request
    if [[ "$run_all" == "true" ]]; then
        if [[ -n "$demo" ]]; then
            echo "Cannot specify both --all and a specific demo"
            exit 1
        fi
        run_all_demos "$skip_cleanup"
        exit $?
    fi
    
    # Handle specific demo request
    if [[ -n "$demo" ]]; then
        # Validate demo name
        if [[ ! " ${DEMO_ORDER[*]} " =~ " ${demo} " ]]; then
            print_error "Unknown demo: $demo"
            echo ""
            echo "Available demos:"
            for d in "${DEMO_ORDER[@]}"; do
                echo "  • $d"
            done
            exit 1
        fi
        
        run_demo "$demo" "$skip_cleanup"
        exit $?
    fi
    
    # No specific action requested, show usage
    echo "No demo specified. Use --help for usage information."
    echo ""
    echo "Quick start:"
    echo "  $0 --list           # See available demos"
    echo "  $0 hello-world      # Run basic demo"
    echo "  $0 --all            # Run all demos"
}

# Run main function
main "$@"