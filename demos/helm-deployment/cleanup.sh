#!/bin/bash

# TMC Helm Deployment Demo Cleanup Script
# Removes all resources created by the helm-deployment demo

set -e

# Demo configuration
DEMO_NAME="helm-deployment"
DEMO_DIR="$(dirname "$0")"

# Cluster names (must match the demo script)
KCP_CLUSTER_NAME="helm-kcp"
EAST_CLUSTER_NAME="helm-east"
WEST_CLUSTER_NAME="helm-west"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

print_success() {
    echo -e "${GREEN}✅ $1${NC}"
}

print_info() {
    echo -e "${BLUE}ℹ️  $1${NC}"
}

print_warning() {
    echo -e "${YELLOW}⚠️  $1${NC}"
}

print_error() {
    echo -e "${RED}❌ $1${NC}"
}

# Function to show usage
usage() {
    echo "TMC Helm Deployment Demo Cleanup"
    echo ""
    echo "Usage: $0 [options]"
    echo ""
    echo "Options:"
    echo "  --demo-only    Remove only demo workloads, keep clusters"
    echo "  --full         Remove everything including kind clusters (default)"
    echo "  --force        Force cleanup, ignore errors"
    echo "  --help         Show this help message"
    echo ""
    echo "Examples:"
    echo "  $0                    # Full cleanup"
    echo "  $0 --demo-only       # Keep clusters, remove workloads"
    echo "  $0 --force           # Force cleanup ignoring errors"
}

# Function to cleanup demo workloads only
cleanup_demo_workloads() {
    print_info "Removing helm deployment demo workloads..."
    
    # Check if clusters exist and clean up workloads
    if ! kind get clusters | grep -q "^$EAST_CLUSTER_NAME$"; then
        print_warning "East cluster not found, skipping workload cleanup"
    else
        print_info "Cleaning up east cluster workloads..."
        export KUBECONFIG="$DEMO_DIR/kubeconfigs/east-cluster.kubeconfig"
        if [[ -f "$KUBECONFIG" ]]; then
            helm uninstall east-workload 2>/dev/null || true
            helm uninstall east-syncer 2>/dev/null || true
            kubectl delete pods -l demo=$DEMO_NAME 2>/dev/null || true
        fi
    fi
    
    if ! kind get clusters | grep -q "^$WEST_CLUSTER_NAME$"; then
        print_warning "West cluster not found, skipping workload cleanup"
    else
        print_info "Cleaning up west cluster workloads..."
        export KUBECONFIG="$DEMO_DIR/kubeconfigs/west-cluster.kubeconfig"
        if [[ -f "$KUBECONFIG" ]]; then
            helm uninstall west-workload 2>/dev/null || true
            helm uninstall west-syncer 2>/dev/null || true
            kubectl delete pods -l demo=$DEMO_NAME 2>/dev/null || true
        fi
    fi
    
    if ! kind get clusters | grep -q "^$KCP_CLUSTER_NAME$"; then
        print_warning "KCP cluster not found, skipping KCP cleanup"
    else
        print_info "Cleaning up KCP resources..."
        export KUBECONFIG="$DEMO_DIR/kubeconfigs/kcp-admin.kubeconfig"
        if [[ -f "$KUBECONFIG" ]]; then
            helm uninstall kcp-tmc 2>/dev/null || true
            kubectl delete configmaps -l demo=$DEMO_NAME 2>/dev/null || true
        fi
    fi
    
    print_success "Demo workloads cleaned up"
}

# Function to cleanup kind clusters
cleanup_clusters() {
    print_info "Removing kind clusters..."
    
    local clusters_removed=0
    
    if kind get clusters | grep -q "^$KCP_CLUSTER_NAME$"; then
        print_info "Removing KCP cluster..."
        if kind delete cluster --name "$KCP_CLUSTER_NAME" 2>/dev/null; then
            print_success "KCP cluster removed"
            clusters_removed=$((clusters_removed + 1))
        else
            print_error "Failed to remove KCP cluster"
        fi
    else
        print_info "KCP cluster not found"
    fi
    
    if kind get clusters | grep -q "^$EAST_CLUSTER_NAME$"; then
        print_info "Removing east cluster..."
        if kind delete cluster --name "$EAST_CLUSTER_NAME" 2>/dev/null; then
            print_success "East cluster removed"
            clusters_removed=$((clusters_removed + 1))
        else
            print_error "Failed to remove east cluster"
        fi
    else
        print_info "East cluster not found"
    fi
    
    if kind get clusters | grep -q "^$WEST_CLUSTER_NAME$"; then
        print_info "Removing west cluster..."
        if kind delete cluster --name "$WEST_CLUSTER_NAME" 2>/dev/null; then
            print_success "West cluster removed"
            clusters_removed=$((clusters_removed + 1))
        else
            print_error "Failed to remove west cluster"
        fi
    else
        print_info "West cluster not found"
    fi
    
    if [[ $clusters_removed -eq 0 ]]; then
        print_info "No helm deployment demo clusters found"
    else
        print_success "$clusters_removed cluster(s) removed"
    fi
}

# Function to cleanup local files
cleanup_local_files() {
    print_info "Cleaning up local files..."
    
    # Remove generated manifests
    if [[ -d "$DEMO_DIR/manifests" ]]; then
        rm -f "$DEMO_DIR/manifests/kcp-tmc-values.yaml" 2>/dev/null || true
        rm -f "$DEMO_DIR/manifests/east-syncer-values.yaml" 2>/dev/null || true
        rm -f "$DEMO_DIR/manifests/west-syncer-values.yaml" 2>/dev/null || true
        rm -f "$DEMO_DIR/manifests/east-workload-values.yaml" 2>/dev/null || true
        rm -f "$DEMO_DIR/manifests/west-workload-values.yaml" 2>/dev/null || true
        rm -f "$DEMO_DIR/manifests/demo-workload-chart.yaml" 2>/dev/null || true
        rm -rf "$DEMO_DIR/manifests/demo-workload" 2>/dev/null || true
        print_success "Generated manifests removed"
    fi
    
    # Remove kubeconfigs
    if [[ -d "$DEMO_DIR/kubeconfigs" ]]; then
        rm -f "$DEMO_DIR/kubeconfigs"/*.kubeconfig 2>/dev/null || true
        print_success "Kubeconfigs removed"
    fi
    
    # Keep logs for debugging unless forced
    if [[ "$FORCE_CLEANUP" == "true" && -d "$DEMO_DIR/logs" ]]; then
        rm -f "$DEMO_DIR/logs"/demo-*.log 2>/dev/null || true
        print_success "Log files removed"
    fi
}

# Function to verify cleanup
verify_cleanup() {
    print_info "Verifying cleanup..."
    
    local issues=0
    
    # Check for remaining clusters
    if kind get clusters | grep -E "^($KCP_CLUSTER_NAME|$EAST_CLUSTER_NAME|$WEST_CLUSTER_NAME)$"; then
        print_warning "Some demo clusters still exist"
        issues=$((issues + 1))
    fi
    
    # Check for Docker containers
    if docker ps -a --format "table {{.Names}}" | grep -E "$KCP_CLUSTER_NAME|$EAST_CLUSTER_NAME|$WEST_CLUSTER_NAME"; then
        print_warning "Some demo containers still exist"
        issues=$((issues + 1))
    fi
    
    if [[ $issues -eq 0 ]]; then
        print_success "Cleanup verification passed"
    else
        print_warning "Cleanup verification found $issues issue(s)"
        if [[ "$FORCE_CLEANUP" == "true" ]]; then
            print_info "Force cleanup mode - attempting to remove remaining resources..."
            # Force remove any remaining containers
            docker rm -f $(docker ps -aq --filter "name=$KCP_CLUSTER_NAME") 2>/dev/null || true
            docker rm -f $(docker ps -aq --filter "name=$EAST_CLUSTER_NAME") 2>/dev/null || true
            docker rm -f $(docker ps -aq --filter "name=$WEST_CLUSTER_NAME") 2>/dev/null || true
        fi
    fi
}

# Main cleanup function
main() {
    local cleanup_type="full"
    local force_cleanup="false"
    
    # Parse command line arguments
    while [[ $# -gt 0 ]]; do
        case $1 in
            --demo-only)
                cleanup_type="demo-only"
                shift
                ;;
            --full)
                cleanup_type="full"
                shift
                ;;
            --force)
                force_cleanup="true"
                FORCE_CLEANUP="true"
                shift
                ;;
            --help)
                usage
                exit 0
                ;;
            *)
                echo "Unknown option: $1"
                usage
                exit 1
                ;;
        esac
    done
    
    echo -e "${BLUE}🧹 TMC Helm Deployment Demo Cleanup${NC}"
    echo ""
    
    if [[ "$force_cleanup" == "true" ]]; then
        print_warning "Force cleanup mode enabled - ignoring errors"
        set +e  # Don't exit on errors in force mode
    fi
    
    case $cleanup_type in
        "demo-only")
            print_info "Performing demo-only cleanup (keeping clusters)..."
            cleanup_demo_workloads
            cleanup_local_files
            ;;
        "full")
            print_info "Performing full cleanup (removing everything)..."
            cleanup_demo_workloads
            cleanup_clusters
            cleanup_local_files
            ;;
    esac
    
    verify_cleanup
    
    echo ""
    print_success "Helm Deployment demo cleanup completed!"
    
    if [[ "$cleanup_type" == "demo-only" ]]; then
        echo ""
        print_info "Clusters preserved for further exploration:"
        echo "• KCP: kubectl --context kind-$KCP_CLUSTER_NAME"
        echo "• East: kubectl --context kind-$EAST_CLUSTER_NAME"
        echo "• West: kubectl --context kind-$WEST_CLUSTER_NAME"
        echo ""
        echo "To remove clusters later: $0 --full"
    fi
}

# Check prerequisites
if ! command -v kind >/dev/null 2>&1; then
    print_error "kind not found. Please install kind to run cleanup."
    exit 1
fi

# Run main function
main "$@"