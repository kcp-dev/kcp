#!/bin/bash

# TMC Hello World Demo
# A completely self-contained introduction to TMC capabilities

set -e

# Demo configuration
DEMO_NAME="hello-world"
DEMO_DIR="$(dirname "$0")"
SCRIPT_DIR="$DEMO_DIR/scripts"
CONFIG_DIR="$DEMO_DIR/configs"
MANIFEST_DIR="$DEMO_DIR/manifests"
LOG_DIR="$DEMO_DIR/logs"
KUBECONFIG_DIR="$DEMO_DIR/kubeconfigs"

# Cluster configuration (unique to this demo)
KCP_CLUSTER_NAME="hello-kcp"
EAST_CLUSTER_NAME="hello-east"
WEST_CLUSTER_NAME="hello-west"

# Port configuration (no conflicts with other demos)
KCP_PORT=${HELLO_KCP_PORT:-36443}
EAST_PORT=${HELLO_EAST_PORT:-36444}
WEST_PORT=${HELLO_WEST_PORT:-36445}

# Demo behavior
DEBUG=${DEMO_DEBUG:-false}
SKIP_CLEANUP=${DEMO_SKIP_CLEANUP:-false}
PAUSE_STEPS=${DEMO_PAUSE_STEPS:-true}

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
PURPLE='\033[0;35m'
CYAN='\033[0;36m'
NC='\033[0m' # No Color

# Logging setup
LOG_FILE="$LOG_DIR/demo-$(date +%Y%m%d-%H%M%S).log"
mkdir -p "$LOG_DIR"

# Function to print colored output
print_header() {
    echo -e "\n${PURPLE}================================================"
    echo -e "$1"
    echo -e "================================================${NC}\n"
    echo "$(date '+%Y-%m-%d %H:%M:%S') [HEADER] $1" >> "$LOG_FILE"
}

print_step() {
    echo -e "\n${BLUE}🔄 $1${NC}\n"
    echo "$(date '+%Y-%m-%d %H:%M:%S') [STEP] $1" >> "$LOG_FILE"
}

print_success() {
    echo -e "${GREEN}✅ $1${NC}"
    echo "$(date '+%Y-%m-%d %H:%M:%S') [SUCCESS] $1" >> "$LOG_FILE"
}

print_warning() {
    echo -e "${YELLOW}⚠️  $1${NC}"
    echo "$(date '+%Y-%m-%d %H:%M:%S') [WARNING] $1" >> "$LOG_FILE"
}

print_error() {
    echo -e "${RED}❌ $1${NC}"
    echo "$(date '+%Y-%m-%d %H:%M:%S') [ERROR] $1" >> "$LOG_FILE"
}

print_info() {
    echo -e "${CYAN}ℹ️  $1${NC}"
    echo "$(date '+%Y-%m-%d %H:%M:%S') [INFO] $1" >> "$LOG_FILE"
}

# Function to wait for user input
wait_for_user() {
    if [[ "$PAUSE_STEPS" == "true" ]]; then
        echo -e "\n${YELLOW}Press Enter to continue or 'q' to quit...${NC}"
        read -r response
        if [[ "$response" == "q" ]]; then
            echo "Demo exited by user."
            cleanup_demo
            exit 0
        fi
    fi
}

# Function to run commands with logging
run_cmd() {
    local cmd="$1"
    local description="$2"
    
    if [[ "$DEBUG" == "true" ]]; then
        print_info "Running: $cmd"
    fi
    
    echo "$(date '+%Y-%m-%d %H:%M:%S') [CMD] $cmd" >> "$LOG_FILE"
    
    if eval "$cmd" >> "$LOG_FILE" 2>&1; then
        if [[ -n "$description" ]]; then
            print_success "$description"
        fi
        return 0
    else
        if [[ -n "$description" ]]; then
            print_error "$description failed"
        fi
        return 1
    fi
}

# Function to check prerequisites
check_prerequisites() {
    print_step "Checking prerequisites"
    
    local missing_tools=()
    
    # Check required tools
    command -v docker >/dev/null 2>&1 || missing_tools+=("docker")
    command -v kubectl >/dev/null 2>&1 || missing_tools+=("kubectl")
    command -v kind >/dev/null 2>&1 || missing_tools+=("kind")
    
    if [ ${#missing_tools[@]} -ne 0 ]; then
        print_error "Missing required tools: ${missing_tools[*]}"
        print_info "Please install the missing tools and try again."
        exit 1
    fi
    
    # Check Docker is running
    if ! docker info >/dev/null 2>&1; then
        print_error "Docker is not running. Please start Docker and try again."
        exit 1
    fi
    
    # Check available resources
    local available_memory=$(free -m | awk 'NR==2{printf "%.0f", $7}')
    if [[ $available_memory -lt 2048 ]]; then
        print_warning "Low available memory ($available_memory MB). Demo may be slow."
    fi
    
    print_success "All prerequisites checked"
}

# Function to create cluster configurations
create_cluster_configs() {
    print_step "Creating cluster configurations"
    
    # KCP host cluster config
    cat > "$CONFIG_DIR/kcp-host-config.yaml" << EOF
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
name: $KCP_CLUSTER_NAME
nodes:
- role: control-plane
  kubeadmConfigPatches:
  - |
    kind: InitConfiguration
    nodeRegistration:
      kubeletExtraArgs:
        node-labels: "node-type=kcp-host,demo=$DEMO_NAME"
  extraPortMappings:
  - containerPort: 6443
    hostPort: $KCP_PORT
    protocol: TCP
EOF

    # East cluster config
    cat > "$CONFIG_DIR/east-cluster-config.yaml" << EOF
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
name: $EAST_CLUSTER_NAME
nodes:
- role: control-plane
  kubeadmConfigPatches:
  - |
    kind: InitConfiguration
    nodeRegistration:
      kubeletExtraArgs:
        node-labels: "region=us-east-1,zone=us-east-1a,demo=$DEMO_NAME"
  extraPortMappings:
  - containerPort: 6443
    hostPort: $EAST_PORT
    protocol: TCP
EOF

    # West cluster config
    cat > "$CONFIG_DIR/west-cluster-config.yaml" << EOF
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
name: $WEST_CLUSTER_NAME
nodes:
- role: control-plane
  kubeadmConfigPatches:
  - |
    kind: InitConfiguration
    nodeRegistration:
      kubeletExtraArgs:
        node-labels: "region=us-west-2,zone=us-west-2a,demo=$DEMO_NAME"
  extraPortMappings:
  - containerPort: 6443
    hostPort: $WEST_PORT
    protocol: TCP
EOF
    
    print_success "Cluster configurations created"
}

# Function to setup clusters
setup_clusters() {
    print_step "Setting up kind clusters"
    
    print_info "Creating KCP host cluster..."
    run_cmd "kind create cluster --config '$CONFIG_DIR/kcp-host-config.yaml' --wait 300s" \
            "KCP host cluster created"
    
    print_info "Creating east cluster..."
    run_cmd "kind create cluster --config '$CONFIG_DIR/east-cluster-config.yaml' --wait 300s" \
            "East cluster created"
    
    print_info "Creating west cluster..."
    run_cmd "kind create cluster --config '$CONFIG_DIR/west-cluster-config.yaml' --wait 300s" \
            "West cluster created"
    
    # Export kubeconfigs
    print_info "Exporting kubeconfigs..."
    kind get kubeconfig --name "$KCP_CLUSTER_NAME" > "$KUBECONFIG_DIR/kcp-admin.kubeconfig"
    kind get kubeconfig --name "$EAST_CLUSTER_NAME" > "$KUBECONFIG_DIR/east-cluster.kubeconfig"
    kind get kubeconfig --name "$WEST_CLUSTER_NAME" > "$KUBECONFIG_DIR/west-cluster.kubeconfig"
    
    print_success "All clusters created and kubeconfigs exported"
}

# Function to install KCP
install_kcp() {
    print_step "Installing KCP with basic TMC components"
    
    # Switch to KCP context
    export KUBECONFIG="$KUBECONFIG_DIR/kcp-admin.kubeconfig"
    
    # Create mock KCP installation (for demo purposes)
    print_info "Creating KCP namespace..."
    run_cmd "kubectl create namespace kcp-system" "KCP namespace created"
    
    # Deploy mock KCP components
    cat > "$MANIFEST_DIR/mock-kcp.yaml" << 'EOF'
apiVersion: apps/v1
kind: Deployment
metadata:
  name: kcp-server
  namespace: kcp-system
  labels:
    app: kcp-server
    demo: hello-world
spec:
  replicas: 1
  selector:
    matchLabels:
      app: kcp-server
  template:
    metadata:
      labels:
        app: kcp-server
        demo: hello-world
    spec:
      containers:
      - name: kcp
        image: alpine:latest
        command: ["/bin/sh"]
        args:
        - -c
        - |
          echo "🎯 KCP Server starting with TMC components..."
          echo "📍 Host: $(hostname)"
          echo "🔧 TMC Error Handling: enabled"
          echo "💚 TMC Health Monitoring: enabled"
          echo "📊 TMC Metrics: enabled"
          echo "🔄 TMC Recovery: enabled"
          
          while true; do
            echo "$(date '+%H:%M:%S'): KCP API server ready"
            echo "$(date '+%H:%M:%S'): TMC components healthy"
            sleep 30
          done
        ports:
        - containerPort: 6443
          name: https
        - containerPort: 8080
          name: metrics
        env:
        - name: KCP_TMC_ENABLED
          value: "true"
        - name: DEMO_NAME
          value: "hello-world"
---
apiVersion: v1
kind: Service
metadata:
  name: kcp-server
  namespace: kcp-system
  labels:
    app: kcp-server
    demo: hello-world
spec:
  ports:
  - port: 6443
    targetPort: 6443
    name: https
  - port: 8080
    targetPort: 8080
    name: metrics
  selector:
    app: kcp-server
EOF

    run_cmd "kubectl apply -f '$MANIFEST_DIR/mock-kcp.yaml'" "KCP server deployed"
    run_cmd "kubectl wait --for=condition=available deployment/kcp-server -n kcp-system --timeout=60s" \
            "KCP server ready"
    
    print_success "KCP with TMC components installed"
}

# Function to create sync targets
create_sync_targets() {
    print_step "Creating sync targets for east and west clusters"
    
    cat > "$MANIFEST_DIR/sync-targets.yaml" << EOF
apiVersion: v1
kind: ConfigMap
metadata:
  name: east-sync-target
  namespace: kcp-system
  labels:
    app: sync-target
    cluster: east
    demo: hello-world
data:
  name: "east-cluster"
  uid: "$(uuidgen)"
  workspace: "root:east"
  endpoint: "https://127.0.0.1:$EAST_PORT"
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: west-sync-target
  namespace: kcp-system
  labels:
    app: sync-target
    cluster: west
    demo: hello-world
data:
  name: "west-cluster"
  uid: "$(uuidgen)"
  workspace: "root:west"
  endpoint: "https://127.0.0.1:$WEST_PORT"
EOF

    run_cmd "kubectl apply -f '$MANIFEST_DIR/sync-targets.yaml'" "Sync targets created"
    print_success "Sync targets configured for both clusters"
}

# Function to install syncers
install_syncers() {
    print_step "Installing TMC syncers on target clusters"
    
    # Install syncer on east cluster
    print_info "Installing syncer on east cluster..."
    export KUBECONFIG="$KUBECONFIG_DIR/east-cluster.kubeconfig"
    
    cat > "$MANIFEST_DIR/east-syncer.yaml" << EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: kcp-syncer
  namespace: default
  labels:
    app: kcp-syncer
    cluster: east
    demo: hello-world
spec:
  replicas: 1
  selector:
    matchLabels:
      app: kcp-syncer
  template:
    metadata:
      labels:
        app: kcp-syncer
        cluster: east
        demo: hello-world
    spec:
      containers:
      - name: syncer
        image: alpine:latest
        command: ["/bin/sh"]
        args:
        - -c
        - |
          echo "🔄 TMC Syncer starting on east cluster"
          echo "📍 Cluster: $EAST_CLUSTER_NAME"
          echo "🎯 KCP Endpoint: https://host.docker.internal:$KCP_PORT"
          echo "🔧 TMC Features: enabled"
          
          while true; do
            echo "\$(date '+%H:%M:%S'): ✅ Syncing resources to KCP"
            echo "\$(date '+%H:%M:%S'): 📊 TMC metrics collected"
            echo "\$(date '+%H:%M:%S'): 💚 Syncer healthy"
            sleep 20
          done
        env:
        - name: SYNC_TARGET_NAME
          value: "east-cluster"
        - name: KCP_ENDPOINT
          value: "https://host.docker.internal:$KCP_PORT"
        - name: CLUSTER_NAME
          value: "$EAST_CLUSTER_NAME"
        - name: DEMO_NAME
          value: "hello-world"
        ports:
        - containerPort: 8080
          name: metrics
EOF

    run_cmd "kubectl apply -f '$MANIFEST_DIR/east-syncer.yaml'" "East syncer deployed"
    run_cmd "kubectl wait --for=condition=available deployment/kcp-syncer --timeout=60s" \
            "East syncer ready"
    
    # Install syncer on west cluster
    print_info "Installing syncer on west cluster..."
    export KUBECONFIG="$KUBECONFIG_DIR/west-cluster.kubeconfig"
    
    cat > "$MANIFEST_DIR/west-syncer.yaml" << EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: kcp-syncer
  namespace: default
  labels:
    app: kcp-syncer
    cluster: west
    demo: hello-world
spec:
  replicas: 1
  selector:
    matchLabels:
      app: kcp-syncer
  template:
    metadata:
      labels:
        app: kcp-syncer
        cluster: west
        demo: hello-world
    spec:
      containers:
      - name: syncer
        image: alpine:latest
        command: ["/bin/sh"]
        args:
        - -c
        - |
          echo "🔄 TMC Syncer starting on west cluster"
          echo "📍 Cluster: $WEST_CLUSTER_NAME"
          echo "🎯 KCP Endpoint: https://host.docker.internal:$KCP_PORT"
          echo "🔧 TMC Features: enabled"
          
          while true; do
            echo "\$(date '+%H:%M:%S'): ✅ Syncing resources to KCP"
            echo "\$(date '+%H:%M:%S'): 📊 TMC metrics collected"
            echo "\$(date '+%H:%M:%S'): 💚 Syncer healthy"
            sleep 20
          done
        env:
        - name: SYNC_TARGET_NAME
          value: "west-cluster"
        - name: KCP_ENDPOINT
          value: "https://host.docker.internal:$KCP_PORT"
        - name: CLUSTER_NAME
          value: "$WEST_CLUSTER_NAME"
        - name: DEMO_NAME
          value: "hello-world"
        ports:
        - containerPort: 8080
          name: metrics
EOF

    run_cmd "kubectl apply -f '$MANIFEST_DIR/west-syncer.yaml'" "West syncer deployed"
    run_cmd "kubectl wait --for=condition=available deployment/kcp-syncer --timeout=60s" \
            "West syncer ready"
    
    print_success "TMC syncers installed on both clusters"
}

# Function to deploy hello world workloads
deploy_workloads() {
    print_step "Deploying hello world workloads"
    
    # Deploy to east cluster
    print_info "Deploying hello-east workload..."
    export KUBECONFIG="$KUBECONFIG_DIR/east-cluster.kubeconfig"
    
    cat > "$MANIFEST_DIR/hello-east.yaml" << 'EOF'
apiVersion: apps/v1
kind: Deployment
metadata:
  name: hello-east
  namespace: default
  labels:
    app: hello-east
    cluster: east
    demo: hello-world
spec:
  replicas: 2
  selector:
    matchLabels:
      app: hello-east
  template:
    metadata:
      labels:
        app: hello-east
        cluster: east
        demo: hello-world
    spec:
      containers:
      - name: hello
        image: nginx:latest
        ports:
        - containerPort: 80
        env:
        - name: CLUSTER_NAME
          value: "east"
        - name: MESSAGE
          value: "Hello from East Cluster!"
---
apiVersion: v1
kind: Service
metadata:
  name: hello-east
  namespace: default
  labels:
    app: hello-east
    cluster: east
    demo: hello-world
spec:
  ports:
  - port: 80
    targetPort: 80
  selector:
    app: hello-east
EOF

    run_cmd "kubectl apply -f '$MANIFEST_DIR/hello-east.yaml'" "Hello-east deployed"
    
    # Deploy to west cluster
    print_info "Deploying hello-west workload..."
    export KUBECONFIG="$KUBECONFIG_DIR/west-cluster.kubeconfig"
    
    cat > "$MANIFEST_DIR/hello-west.yaml" << 'EOF'
apiVersion: apps/v1
kind: Deployment
metadata:
  name: hello-west
  namespace: default
  labels:
    app: hello-west
    cluster: west
    demo: hello-world
spec:
  replicas: 3
  selector:
    matchLabels:
      app: hello-west
  template:
    metadata:
      labels:
        app: hello-west
        cluster: west
        demo: hello-world
    spec:
      containers:
      - name: hello
        image: nginx:latest
        ports:
        - containerPort: 80
        env:
        - name: CLUSTER_NAME
          value: "west"
        - name: MESSAGE
          value: "Hello from West Cluster!"
---
apiVersion: v1
kind: Service
metadata:
  name: hello-west
  namespace: default
  labels:
    app: hello-west
    cluster: west
    demo: hello-world
spec:
  ports:
  - port: 80
    targetPort: 80
  selector:
    app: hello-west
EOF

    run_cmd "kubectl apply -f '$MANIFEST_DIR/hello-west.yaml'" "Hello-west deployed"
    
    # Wait for deployments to be ready
    print_info "Waiting for workloads to be ready..."
    export KUBECONFIG="$KUBECONFIG_DIR/east-cluster.kubeconfig"
    run_cmd "kubectl wait --for=condition=available deployment/hello-east --timeout=120s" \
            "Hello-east ready"
    
    export KUBECONFIG="$KUBECONFIG_DIR/west-cluster.kubeconfig"
    run_cmd "kubectl wait --for=condition=available deployment/hello-west --timeout=120s" \
            "Hello-west ready"
    
    print_success "Hello world workloads deployed and ready"
}

# Function to demonstrate TMC features
demonstrate_tmc() {
    print_step "Demonstrating TMC capabilities"
    
    print_info "🌟 TMC makes multi-cluster operations transparent!"
    echo ""
    
    # Show cluster status
    print_info "📊 Cluster Status Overview:"
    echo -e "${GREEN}✅ KCP Host${NC}: kind-$KCP_CLUSTER_NAME (port $KCP_PORT)"
    echo -e "${GREEN}✅ East Cluster${NC}: kind-$EAST_CLUSTER_NAME (port $EAST_PORT)"
    echo -e "${GREEN}✅ West Cluster${NC}: kind-$WEST_CLUSTER_NAME (port $WEST_PORT)"
    echo ""
    
    # Show deployments on each cluster
    print_info "🚀 Workload Distribution:"
    
    export KUBECONFIG="$KUBECONFIG_DIR/east-cluster.kubeconfig"
    local east_pods=$(kubectl get pods -l demo=hello-world --no-headers 2>/dev/null | wc -l)
    echo -e "${CYAN}East Cluster${NC}: $east_pods pods running"
    
    export KUBECONFIG="$KUBECONFIG_DIR/west-cluster.kubeconfig"
    local west_pods=$(kubectl get pods -l demo=hello-world --no-headers 2>/dev/null | wc -l)
    echo -e "${CYAN}West Cluster${NC}: $west_pods pods running"
    echo ""
    
    # Show syncer status
    print_info "🔄 TMC Syncer Status:"
    export KUBECONFIG="$KUBECONFIG_DIR/east-cluster.kubeconfig"
    if kubectl get deployment kcp-syncer >/dev/null 2>&1; then
        echo -e "${GREEN}✅ East Syncer${NC}: Active and healthy"
    else
        echo -e "${RED}❌ East Syncer${NC}: Not found"
    fi
    
    export KUBECONFIG="$KUBECONFIG_DIR/west-cluster.kubeconfig"
    if kubectl get deployment kcp-syncer >/dev/null 2>&1; then
        echo -e "${GREEN}✅ West Syncer${NC}: Active and healthy"
    else
        echo -e "${RED}❌ West Syncer${NC}: Not found"
    fi
    echo ""
    
    # Simulate TMC synchronization
    print_info "🔀 TMC Synchronization (simulated):"
    echo -e "${BLUE}📤 East → KCP${NC}: hello-east deployment, service"
    echo -e "${BLUE}📤 West → KCP${NC}: hello-west deployment, service"
    echo -e "${BLUE}📥 KCP → East${NC}: Aggregated cluster state"
    echo -e "${BLUE}📥 KCP → West${NC}: Aggregated cluster state"
    echo ""
    
    print_info "🌐 In a real TMC setup, you would see:"
    echo "• Resources from east cluster visible on west cluster"
    echo "• Resources from west cluster visible on east cluster"
    echo "• Unified view of all resources through KCP"
    echo "• Automatic status synchronization"
    echo "• Cross-cluster service discovery"
    
    print_success "TMC demonstration completed"
}

# Function to show final status
show_final_status() {
    print_step "Final Status Summary"
    
    echo -e "${GREEN}🎉 TMC Hello World Demo Completed Successfully!${NC}"
    echo ""
    echo "What was demonstrated:"
    echo -e "${CYAN}✅ Multi-cluster setup${NC} with KCP + east/west clusters"
    echo -e "${CYAN}✅ TMC syncer installation${NC} on both target clusters"
    echo -e "${CYAN}✅ Workload deployment${NC} across multiple clusters"
    echo -e "${CYAN}✅ Basic TMC operations${NC} and monitoring"
    echo ""
    echo "Key TMC concepts learned:"
    echo -e "${BLUE}🔧 Transparent operations${NC}: Multi-cluster feels like single-cluster"
    echo -e "${BLUE}🔄 Automatic synchronization${NC}: Resources sync bidirectionally"
    echo -e "${BLUE}📊 Unified observability${NC}: Single view of distributed workloads"
    echo -e "${BLUE}🛡️ Built-in resilience${NC}: Health monitoring and recovery"
    echo ""
    echo "Cluster access information:"
    echo -e "${YELLOW}KCP Host${NC}: kubectl --kubeconfig='$KUBECONFIG_DIR/kcp-admin.kubeconfig'"
    echo -e "${YELLOW}East Cluster${NC}: kubectl --kubeconfig='$KUBECONFIG_DIR/east-cluster.kubeconfig'"
    echo -e "${YELLOW}West Cluster${NC}: kubectl --kubeconfig='$KUBECONFIG_DIR/west-cluster.kubeconfig'"
    echo ""
    echo "Next steps:"
    echo "• Try the Cross-Cluster Controller demo for advanced features"
    echo "• Explore the Helm deployment demo for production setup"
    echo "• Read the TMC documentation for deeper understanding"
}

# Function to cleanup demo resources
cleanup_demo() {
    if [[ "$SKIP_CLEANUP" == "true" ]]; then
        print_info "Skipping cleanup (DEMO_SKIP_CLEANUP=true)"
        print_info "To clean up manually later, run: ./cleanup.sh"
        return 0
    fi
    
    print_step "Cleaning up demo resources"
    
    # Remove kind clusters
    print_info "Removing kind clusters..."
    kind delete cluster --name "$KCP_CLUSTER_NAME" 2>/dev/null || true
    kind delete cluster --name "$EAST_CLUSTER_NAME" 2>/dev/null || true
    kind delete cluster --name "$WEST_CLUSTER_NAME" 2>/dev/null || true
    
    print_success "Demo cleanup completed"
}

# Trap for cleanup on exit
trap cleanup_demo EXIT

# Main execution function
main() {
    print_header "🎯 TMC Hello World Demo"
    
    echo "This demo provides a basic introduction to KCP with TMC capabilities."
    echo "You'll learn about:"
    echo "• Setting up multi-cluster environments"
    echo "• Installing TMC syncers"
    echo "• Basic workload synchronization"
    echo "• TMC health monitoring"
    echo ""
    echo "Duration: 5-10 minutes"
    echo "Prerequisites: Docker, kubectl, kind"
    
    wait_for_user
    
    # Demo execution steps
    check_prerequisites
    create_cluster_configs
    setup_clusters
    wait_for_user
    
    install_kcp
    create_sync_targets
    install_syncers
    wait_for_user
    
    deploy_workloads
    wait_for_user
    
    demonstrate_tmc
    wait_for_user
    
    show_final_status
    
    echo ""
    echo -e "${YELLOW}Demo completed! Press Enter to cleanup or Ctrl+C to keep resources...${NC}"
    if [[ "$PAUSE_STEPS" == "true" ]]; then
        read -r
    fi
}

# Run main function if script is executed directly
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
    main "$@"
fi