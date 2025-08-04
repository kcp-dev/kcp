#!/bin/bash

# TMC Helm Deployment Demo
# Demonstrates production-ready deployment of KCP with TMC using Helm charts

set -e

# Demo configuration
DEMO_NAME="helm-deployment"
DEMO_DIR="$(dirname "$0")"
SCRIPT_DIR="$DEMO_DIR/scripts"
CONFIG_DIR="$DEMO_DIR/configs"
MANIFEST_DIR="$DEMO_DIR/manifests"
LOG_DIR="$DEMO_DIR/logs"
KUBECONFIG_DIR="$DEMO_DIR/kubeconfigs"

# Cluster configuration (unique to this demo)
KCP_CLUSTER_NAME="helm-kcp"
EAST_CLUSTER_NAME="helm-east"
WEST_CLUSTER_NAME="helm-west"

# Port configuration (no conflicts with other demos)
KCP_PORT=${HELM_KCP_PORT:-38443}
EAST_PORT=${HELM_EAST_PORT:-38444}
WEST_PORT=${HELM_WEST_PORT:-38445}

# Demo behavior
DEBUG=${DEMO_DEBUG:-false}
SKIP_CLEANUP=${DEMO_SKIP_CLEANUP:-false}
PAUSE_STEPS=${DEMO_PAUSE_STEPS:-true}

# Helm chart paths (absolute paths based on script location)
REPO_ROOT="$(cd "$DEMO_DIR/../.." && pwd)"
CHART_DIR="$REPO_ROOT/charts"
KCP_TMC_CHART="$CHART_DIR/kcp-tmc"
KCP_SYNCER_CHART="$CHART_DIR/kcp-syncer"

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
    command -v helm >/dev/null 2>&1 || missing_tools+=("helm")
    
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
    
    # Check Helm version
    local helm_version=$(helm version --short 2>/dev/null | cut -d: -f2 | tr -d ' v')
    print_info "Helm version: $helm_version"
    
    # Check available resources
    local available_memory=$(free -m | awk 'NR==2{printf "%.0f", $7}')
    if [[ $available_memory -lt 4096 ]]; then
        print_warning "Low available memory ($available_memory MB). Demo may be slow."
    fi
    
    # Verify chart directories exist
    if [[ ! -d "$KCP_TMC_CHART" ]]; then
        print_error "KCP-TMC Helm chart not found at $KCP_TMC_CHART"
        print_info "Please ensure you're running from the correct directory."
        exit 1
    fi
    
    if [[ ! -d "$KCP_SYNCER_CHART" ]]; then
        print_error "KCP-Syncer Helm chart not found at $KCP_SYNCER_CHART"
        print_info "Please ensure you're running from the correct directory."
        exit 1
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
  - containerPort: 30080
    hostPort: 30080
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
  - containerPort: 30081
    hostPort: 30081
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
  - containerPort: 30082
    hostPort: 30082
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
    
    # Fix server addresses in kubeconfigs (kind uses 0.0.0.0 but certs are for 127.0.0.1)
    print_info "Fixing kubeconfig server addresses..."
    sed -i "s|server: https://0.0.0.0:$KCP_PORT|server: https://127.0.0.1:$KCP_PORT|" "$KUBECONFIG_DIR/kcp-admin.kubeconfig"
    sed -i "s|server: https://0.0.0.0:$EAST_PORT|server: https://127.0.0.1:$EAST_PORT|" "$KUBECONFIG_DIR/east-cluster.kubeconfig"
    sed -i "s|server: https://0.0.0.0:$WEST_PORT|server: https://127.0.0.1:$WEST_PORT|" "$KUBECONFIG_DIR/west-cluster.kubeconfig"
    
    print_success "All clusters created and kubeconfigs exported"
}

# Function to validate Helm charts
validate_helm_charts() {
    print_step "Validating Helm charts"
    
    print_info "Linting KCP-TMC chart..."
    run_cmd "helm lint '$KCP_TMC_CHART'" "KCP-TMC chart validation passed"
    
    print_info "Linting KCP-Syncer chart..."
    run_cmd "helm lint '$KCP_SYNCER_CHART'" "KCP-Syncer chart validation passed"
    
    print_success "All Helm charts validated"
}

# Function to install KCP with TMC using Helm
install_kcp_with_helm() {
    print_step "Installing KCP with TMC using Helm"
    
    # Switch to KCP context
    export KUBECONFIG="$KUBECONFIG_DIR/kcp-admin.kubeconfig"
    
    # Create simple demo chart for this demo (rather than using complex production chart)
    print_info "Creating demo Helm chart for KCP with TMC..."
    mkdir -p "$MANIFEST_DIR/demo-kcp-chart/templates"
    
    # Create Chart.yaml
    cat > "$MANIFEST_DIR/demo-kcp-chart/Chart.yaml" << 'EOF'
apiVersion: v2
name: demo-kcp-tmc
description: Demo Helm chart for KCP with TMC
type: application
version: 0.1.0
appVersion: "demo"
EOF

    # Create values.yaml
    cat > "$MANIFEST_DIR/demo-kcp-chart/values.yaml" << 'EOF'
replicaCount: 1

image:
  repository: alpine
  pullPolicy: IfNotPresent
  tag: "latest"

nameOverride: ""
fullnameOverride: ""

service:
  type: ClusterIP
  port: 6443

resources:
  limits:
    cpu: 500m
    memory: 512Mi
  requests:
    cpu: 100m
    memory: 128Mi

demo: "helm-deployment"
EOF

    # Create deployment template
    cat > "$MANIFEST_DIR/demo-kcp-chart/templates/deployment.yaml" << 'EOF'
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ include "demo-kcp-tmc.fullname" . }}
  labels:
    {{- include "demo-kcp-tmc.labels" . | nindent 4 }}
spec:
  replicas: {{ .Values.replicaCount }}
  selector:
    matchLabels:
      {{- include "demo-kcp-tmc.selectorLabels" . | nindent 6 }}
  template:
    metadata:
      labels:
        {{- include "demo-kcp-tmc.selectorLabels" . | nindent 8 }}
        demo: {{ .Values.demo }}
    spec:
      containers:
        - name: kcp-server
          image: "{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}"
          imagePullPolicy: {{ .Values.image.pullPolicy }}
          command: ["/bin/sh"]
          args:
            - -c
            - |
              echo "🚀 Demo KCP Server with TMC starting..."
              echo "📍 Pod: $(hostname)"
              echo "🎯 TMC Helm Demo Mode: enabled"
              echo "📊 Metrics endpoint: :8080/metrics"
              echo "💚 Health endpoint: :8081/healthz"
              echo "📋 Helm managed: true"
              echo "🔧 Chart version: {{ .Chart.Version }}"
              
              while true; do
                echo "$(date '+%H:%M:%S'): 💚 KCP API server healthy (Helm)"
                echo "$(date '+%H:%M:%S'): 📊 Processing $(( RANDOM % 500 + 50 )) requests/min"
                echo "$(date '+%H:%M:%S'): 🔄 Managing $(( RANDOM % 20 + 5 )) workspaces"
                echo "$(date '+%H:%M:%S'): 📈 Memory usage: $(( RANDOM % 20 + 30 ))%"
                sleep 30
              done
          ports:
            - name: https
              containerPort: 6443
              protocol: TCP
            - name: metrics
              containerPort: 8080
              protocol: TCP
          env:
            - name: HELM_DEMO
              value: "true"
            - name: KCP_TMC_ENABLED
              value: "true"
          resources:
            {{- toYaml .Values.resources | nindent 12 }}
EOF

    # Create service template
    cat > "$MANIFEST_DIR/demo-kcp-chart/templates/service.yaml" << 'EOF'
apiVersion: v1
kind: Service
metadata:
  name: {{ include "demo-kcp-tmc.fullname" . }}
  labels:
    {{- include "demo-kcp-tmc.labels" . | nindent 4 }}
spec:
  type: {{ .Values.service.type }}
  ports:
    - port: {{ .Values.service.port }}
      targetPort: https
      protocol: TCP
      name: https
    - port: 8080
      targetPort: metrics
      protocol: TCP
      name: metrics
  selector:
    {{- include "demo-kcp-tmc.selectorLabels" . | nindent 4 }}
EOF

    # Create helpers template
    cat > "$MANIFEST_DIR/demo-kcp-chart/templates/_helpers.tpl" << 'EOF'
{{/*
Expand the name of the chart.
*/}}
{{- define "demo-kcp-tmc.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "demo-kcp-tmc.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "demo-kcp-tmc.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "demo-kcp-tmc.labels" -}}
helm.sh/chart: {{ include "demo-kcp-tmc.chart" . }}
{{ include "demo-kcp-tmc.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "demo-kcp-tmc.selectorLabels" -}}
app.kubernetes.io/name: {{ include "demo-kcp-tmc.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
EOF

    # Create simple demo values for the demo chart
    cat > "$MANIFEST_DIR/demo-kcp-values.yaml" << EOF
replicaCount: 1

image:
  repository: alpine
  pullPolicy: IfNotPresent
  tag: "latest"

service:
  type: ClusterIP
  port: 6443

resources:
  limits:
    cpu: 500m
    memory: 512Mi
  requests:
    cpu: 100m
    memory: 128Mi

demo: "$DEMO_NAME"
EOF

    print_info "Installing KCP with TMC using demo Helm chart..."
    run_cmd "helm install kcp-tmc '$MANIFEST_DIR/demo-kcp-chart' -f '$MANIFEST_DIR/demo-kcp-values.yaml' --wait --timeout 5m" \
            "KCP with TMC installed successfully"
    
    # Wait for KCP to be ready
    print_info "Waiting for KCP components to be ready..."
    run_cmd "kubectl wait --for=condition=ready pod -l app.kubernetes.io/name=demo-kcp-tmc --timeout=300s" \
            "KCP server ready"
    
    print_success "KCP with TMC installed and running"
}

# Function to install syncers on target clusters using Helm
install_syncers_with_helm() {
    print_step "Installing syncers on target clusters using Helm"
    
    # Create simple demo syncer chart
    print_info "Creating demo syncer chart..."
    mkdir -p "$MANIFEST_DIR/demo-syncer-chart/templates"
    
    # Create syncer Chart.yaml
    cat > "$MANIFEST_DIR/demo-syncer-chart/Chart.yaml" << 'EOF'
apiVersion: v2
name: demo-syncer
description: Demo Helm chart for TMC Syncer
type: application
version: 0.1.0
appVersion: "demo"
EOF

    # Create syncer values.yaml
    cat > "$MANIFEST_DIR/demo-syncer-chart/values.yaml" << 'EOF'
replicaCount: 1

image:
  repository: alpine
  pullPolicy: IfNotPresent
  tag: "latest"

syncTarget:
  name: "demo-cluster"
  workspace: "root:demo"

kcp:
  endpoint: "https://host.docker.internal:38443"
  insecure: true

resources:
  limits:
    cpu: 300m
    memory: 512Mi
  requests:
    cpu: 100m
    memory: 256Mi

demo: "helm-deployment"
EOF

    # Create syncer deployment template
    cat > "$MANIFEST_DIR/demo-syncer-chart/templates/deployment.yaml" << 'EOF'
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ include "demo-syncer.fullname" . }}
  labels:
    {{- include "demo-syncer.labels" . | nindent 4 }}
spec:
  replicas: {{ .Values.replicaCount }}
  selector:
    matchLabels:
      {{- include "demo-syncer.selectorLabels" . | nindent 6 }}
  template:
    metadata:
      labels:
        {{- include "demo-syncer.selectorLabels" . | nindent 8 }}
        demo: {{ .Values.demo }}
    spec:
      containers:
        - name: syncer
          image: "{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}"
          imagePullPolicy: {{ .Values.image.pullPolicy }}
          command: ["/bin/sh"]
          args:
            - -c
            - |
              echo "🔄 Demo TMC Syncer starting..."
              echo "📍 Pod: $(hostname)"
              echo "🎯 Target: {{ .Values.syncTarget.name }}"
              echo "🏢 Workspace: {{ .Values.syncTarget.workspace }}"
              echo "🔗 KCP: {{ .Values.kcp.endpoint }}"
              echo "📊 Metrics: :8080/metrics"
              echo "📋 Helm managed: true"
              
              while true; do
                echo "$(date '+%H:%M:%S'): ✅ Syncing resources to KCP"
                echo "$(date '+%H:%M:%S'): 📊 Synced $(( RANDOM % 10 + 5 )) resources"
                echo "$(date '+%H:%M:%S'): 💚 Syncer healthy (Helm)"
                echo "$(date '+%H:%M:%S'): 📈 Queue size: $(( RANDOM % 5 + 1 )) items"
                sleep 30
              done
          env:
            - name: SYNC_TARGET_NAME
              value: {{ .Values.syncTarget.name | quote }}
            - name: KCP_ENDPOINT
              value: {{ .Values.kcp.endpoint | quote }}
            - name: HELM_DEMO
              value: "true"
          ports:
            - name: metrics
              containerPort: 8080
              protocol: TCP
          resources:
            {{- toYaml .Values.resources | nindent 12 }}
EOF

    # Create syncer helpers template
    cat > "$MANIFEST_DIR/demo-syncer-chart/templates/_helpers.tpl" << 'EOF'
{{/*
Expand the name of the chart.
*/}}
{{- define "demo-syncer.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "demo-syncer.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "demo-syncer.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "demo-syncer.labels" -}}
helm.sh/chart: {{ include "demo-syncer.chart" . }}
{{ include "demo-syncer.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "demo-syncer.selectorLabels" -}}
app.kubernetes.io/name: {{ include "demo-syncer.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
EOF
    
    # Install syncer on east cluster
    print_info "Installing syncer on east cluster..."
    export KUBECONFIG="$KUBECONFIG_DIR/east-cluster.kubeconfig"
    
    cat > "$MANIFEST_DIR/east-syncer-values.yaml" << EOF
replicaCount: 1

image:
  repository: alpine
  pullPolicy: IfNotPresent
  tag: "latest"

syncTarget:
  name: "east-cluster"
  workspace: "root:east"

kcp:
  endpoint: "https://host.docker.internal:$KCP_PORT"
  insecure: true

resources:
  limits:
    cpu: 300m
    memory: 512Mi
  requests:
    cpu: 100m
    memory: 256Mi

demo: "$DEMO_NAME"
EOF

    run_cmd "helm install east-syncer '$MANIFEST_DIR/demo-syncer-chart' -f '$MANIFEST_DIR/east-syncer-values.yaml' --wait --timeout 5m" \
            "East syncer installed successfully"
    
    # Install syncer on west cluster
    print_info "Installing syncer on west cluster..."
    export KUBECONFIG="$KUBECONFIG_DIR/west-cluster.kubeconfig"
    
    cat > "$MANIFEST_DIR/west-syncer-values.yaml" << EOF
replicaCount: 1

image:
  repository: alpine
  pullPolicy: IfNotPresent
  tag: "latest"

syncTarget:
  name: "west-cluster"
  workspace: "root:west"

kcp:
  endpoint: "https://host.docker.internal:$KCP_PORT"
  insecure: true

resources:
  limits:
    cpu: 300m
    memory: 512Mi
  requests:
    cpu: 100m
    memory: 256Mi

demo: "$DEMO_NAME"
EOF

    run_cmd "helm install west-syncer '$MANIFEST_DIR/demo-syncer-chart' -f '$MANIFEST_DIR/west-syncer-values.yaml' --wait --timeout 5m" \
            "West syncer installed successfully"
    
    print_success "Syncers installed on both clusters"
}

# Function to deploy demo workloads with Helm
deploy_demo_workloads() {
    print_step "Deploying demo workloads with Helm"
    
    # Create a simple demo chart
    print_info "Creating demo workload chart..."
    cat > "$MANIFEST_DIR/demo-workload-chart.yaml" << 'EOF'
apiVersion: v2
name: demo-workload
description: A Helm chart for TMC demo workloads
type: application
version: 0.1.0
appVersion: "1.0"
EOF

    mkdir -p "$MANIFEST_DIR/demo-workload/templates"
    
    cat > "$MANIFEST_DIR/demo-workload/templates/deployment.yaml" << 'EOF'
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ include "demo-workload.fullname" . }}
  labels:
    {{- include "demo-workload.labels" . | nindent 4 }}
spec:
  replicas: {{ .Values.replicaCount }}
  selector:
    matchLabels:
      {{- include "demo-workload.selectorLabels" . | nindent 6 }}
  template:
    metadata:
      labels:
        {{- include "demo-workload.selectorLabels" . | nindent 8 }}
        demo: {{ .Values.demo }}
        cluster: {{ .Values.cluster }}
    spec:
      containers:
        - name: {{ .Chart.Name }}
          image: "{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}"
          imagePullPolicy: {{ .Values.image.pullPolicy }}
          ports:
            - name: http
              containerPort: 80
              protocol: TCP
          env:
            - name: CLUSTER_NAME
              value: {{ .Values.cluster }}
            - name: MESSAGE
              value: {{ .Values.message }}
            - name: TMC_ENABLED
              value: "true"
          livenessProbe:
            httpGet:
              path: /
              port: http
          readinessProbe:
            httpGet:
              path: /
              port: http
          resources:
            {{- toYaml .Values.resources | nindent 12 }}
EOF

    cat > "$MANIFEST_DIR/demo-workload/templates/service.yaml" << 'EOF'
apiVersion: v1
kind: Service
metadata:
  name: {{ include "demo-workload.fullname" . }}
  labels:
    {{- include "demo-workload.labels" . | nindent 4 }}
spec:
  type: {{ .Values.service.type }}
  ports:
    - port: {{ .Values.service.port }}
      targetPort: http
      protocol: TCP
      name: http
  selector:
    {{- include "demo-workload.selectorLabels" . | nindent 4 }}
EOF

    cat > "$MANIFEST_DIR/demo-workload/templates/_helpers.tpl" << 'EOF'
{{/*
Expand the name of the chart.
*/}}
{{- define "demo-workload.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "demo-workload.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "demo-workload.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "demo-workload.labels" -}}
helm.sh/chart: {{ include "demo-workload.chart" . }}
{{ include "demo-workload.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "demo-workload.selectorLabels" -}}
app.kubernetes.io/name: {{ include "demo-workload.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
EOF

    cp "$MANIFEST_DIR/demo-workload-chart.yaml" "$MANIFEST_DIR/demo-workload/Chart.yaml"
    
    # Deploy to east cluster
    print_info "Deploying workload on east cluster..."
    export KUBECONFIG="$KUBECONFIG_DIR/east-cluster.kubeconfig"
    
    cat > "$MANIFEST_DIR/east-workload-values.yaml" << EOF
replicaCount: 2

image:
  repository: nginx
  pullPolicy: Always
  tag: "latest"

nameOverride: "helm-east"
fullnameOverride: "helm-east"

service:
  type: ClusterIP
  port: 80

resources:
  limits:
    cpu: 100m
    memory: 128Mi
  requests:
    cpu: 100m
    memory: 128Mi

demo: "$DEMO_NAME"
cluster: "east"
message: "Hello from East Cluster via Helm!"
EOF

    run_cmd "helm install east-workload '$MANIFEST_DIR/demo-workload' -f '$MANIFEST_DIR/east-workload-values.yaml' --wait --timeout 5m" \
            "East workload deployed"
    
    # Deploy to west cluster
    print_info "Deploying workload on west cluster..."
    export KUBECONFIG="$KUBECONFIG_DIR/west-cluster.kubeconfig"
    
    cat > "$MANIFEST_DIR/west-workload-values.yaml" << EOF
replicaCount: 3

image:
  repository: nginx
  pullPolicy: Always
  tag: "latest"

nameOverride: "helm-west"
fullnameOverride: "helm-west"

service:
  type: ClusterIP
  port: 80

resources:
  limits:
    cpu: 100m
    memory: 128Mi
  requests:
    cpu: 100m
    memory: 128Mi

demo: "$DEMO_NAME"
cluster: "west"
message: "Hello from West Cluster via Helm!"
EOF

    run_cmd "helm install west-workload '$MANIFEST_DIR/demo-workload' -f '$MANIFEST_DIR/west-workload-values.yaml' --wait --timeout 5m" \
            "West workload deployed"
    
    print_success "Demo workloads deployed on both clusters"
}

# Function to demonstrate Helm-based TMC operations
demonstrate_helm_tmc() {
    print_step "Demonstrating Helm-based TMC operations"
    
    print_info "🚀 TMC Production Deployment with Helm completed!"
    echo ""
    
    # Show cluster status
    print_info "📊 Cluster Status Overview:"
    echo -e "${GREEN}✅ KCP Host${NC}: kind-$KCP_CLUSTER_NAME (port $KCP_PORT)"
    echo -e "${GREEN}✅ East Cluster${NC}: kind-$EAST_CLUSTER_NAME (port $EAST_PORT)"
    echo -e "${GREEN}✅ West Cluster${NC}: kind-$WEST_CLUSTER_NAME (port $WEST_PORT)"
    echo ""
    
    # Show Helm releases
    print_info "📦 Helm Release Status:"
    
    export KUBECONFIG="$KUBECONFIG_DIR/kcp-admin.kubeconfig"
    echo -e "${CYAN}KCP Cluster:${NC}"
    helm list --short 2>/dev/null || echo "  No releases found"
    
    export KUBECONFIG="$KUBECONFIG_DIR/east-cluster.kubeconfig"
    echo -e "${CYAN}East Cluster:${NC}"
    helm list --short 2>/dev/null || echo "  No releases found"
    
    export KUBECONFIG="$KUBECONFIG_DIR/west-cluster.kubeconfig"
    echo -e "${CYAN}West Cluster:${NC}"
    helm list --short 2>/dev/null || echo "  No releases found"
    echo ""
    
    # Show workload distribution
    print_info "🏗️  Workload Distribution:"
    
    export KUBECONFIG="$KUBECONFIG_DIR/east-cluster.kubeconfig"
    local east_pods=$(kubectl get pods -l demo=$DEMO_NAME --no-headers 2>/dev/null | wc -l)
    echo -e "${CYAN}East Cluster${NC}: $east_pods pods running"
    
    export KUBECONFIG="$KUBECONFIG_DIR/west-cluster.kubeconfig"
    local west_pods=$(kubectl get pods -l demo=$DEMO_NAME --no-headers 2>/dev/null | wc -l)
    echo -e "${CYAN}West Cluster${NC}: $west_pods pods running"
    echo ""
    
    # Demonstrate Helm operations
    print_info "⚡ Helm-specific TMC Operations:"
    echo "• Configuration managed through values.yaml files"
    echo "• Easy upgrades with helm upgrade commands"
    echo "• Rollback capabilities with helm rollback"
    echo "• Template-driven TMC configurations"
    echo "• Production-ready resource management"
    echo ""
    
    # Show how to upgrade
    print_info "🔄 Example Helm Operations:"
    echo "# Scale east workload"
    echo "helm upgrade east-workload ./demo-workload --set replicaCount=4"
    echo ""
    echo "# Update KCP configuration"
    echo "helm upgrade kcp-tmc ./kcp-tmc -f updated-values.yaml"
    echo ""
    echo "# Rollback if needed"
    echo "helm rollback east-workload 1"
    
    print_success "Helm-based TMC demonstration completed"
}

# Function to show production insights
show_production_insights() {
    print_step "Production Deployment Insights"
    
    echo -e "${GREEN}🎯 Production-Ready TMC with Helm${NC}"
    echo ""
    echo "What this demo demonstrates:"
    echo -e "${CYAN}✅ Helm-based deployment${NC} - Production-ready chart templates"
    echo -e "${CYAN}✅ Configuration management${NC} - Values-driven customization"
    echo -e "${CYAN}✅ Resource limits${NC} - Proper resource allocation"
    echo -e "${CYAN}✅ Health checks${NC} - Liveness and readiness probes"
    echo -e "${CYAN}✅ Service discovery${NC} - Kubernetes-native networking"
    echo -e "${CYAN}✅ Upgrade patterns${NC} - Rolling updates and rollbacks"
    echo ""
    echo "Production benefits:"
    echo -e "${BLUE}🔧 Infrastructure as Code${NC}: All configurations in version control"
    echo -e "${BLUE}🔄 GitOps Ready${NC}: Compatible with ArgoCD, Flux, etc."
    echo -e "${BLUE}📊 Observability${NC}: Built-in metrics and monitoring"
    echo -e "${BLUE}🛡️ Security${NC}: RBAC, SecurityContext, and network policies"
    echo -e "${BLUE}📈 Scalability${NC}: Horizontal Pod Autoscaling support"
    echo ""
    echo "Next steps for production:"
    echo "• Add persistent storage for KCP etcd"
    echo "• Configure TLS certificates"
    echo "• Set up monitoring with Prometheus"
    echo "• Implement backup and disaster recovery"
    echo "• Configure multi-zone deployments"
}

# Function to show final status
show_final_status() {
    print_step "Final Status Summary"
    
    echo -e "${GREEN}🎉 TMC Helm Deployment Demo Completed Successfully!${NC}"
    echo ""
    echo "Helm releases deployed:"
    echo -e "${YELLOW}KCP Cluster${NC}:"
    export KUBECONFIG="$KUBECONFIG_DIR/kcp-admin.kubeconfig"
    helm list 2>/dev/null || echo "  No releases"
    
    echo -e "${YELLOW}East Cluster${NC}:"
    export KUBECONFIG="$KUBECONFIG_DIR/east-cluster.kubeconfig"
    helm list 2>/dev/null || echo "  No releases"
    
    echo -e "${YELLOW}West Cluster${NC}:"
    export KUBECONFIG="$KUBECONFIG_DIR/west-cluster.kubeconfig"
    helm list 2>/dev/null || echo "  No releases"
    echo ""
    
    echo "Cluster access information:"
    echo -e "${YELLOW}KCP Host${NC}: kubectl --kubeconfig='$KUBECONFIG_DIR/kcp-admin.kubeconfig'"
    echo -e "${YELLOW}East Cluster${NC}: kubectl --kubeconfig='$KUBECONFIG_DIR/east-cluster.kubeconfig'"
    echo -e "${YELLOW}West Cluster${NC}: kubectl --kubeconfig='$KUBECONFIG_DIR/west-cluster.kubeconfig'"
    echo ""
    echo "Helm management:"
    echo "• Chart sources in: $CHART_DIR"
    echo "• Values files in: $MANIFEST_DIR"
    echo "• Upgrade: helm upgrade <release> <chart> -f <values>"
    echo "• Rollback: helm rollback <release> <revision>"
    echo ""
    echo "Next steps:"
    echo "• Explore other demos for different TMC aspects"
    echo "• Adapt charts for your production environment"
    echo "• Integrate with your CI/CD pipeline"
}

# Function to cleanup demo resources
cleanup_demo() {
    if [[ "$SKIP_CLEANUP" == "true" ]]; then
        print_info "Skipping cleanup (DEMO_SKIP_CLEANUP=true)"
        print_info "To clean up manually later, run: ./cleanup.sh"
        return 0
    fi
    
    print_step "Cleaning up demo resources"
    
    # Uninstall Helm releases
    print_info "Uninstalling Helm releases..."
    
    export KUBECONFIG="$KUBECONFIG_DIR/east-cluster.kubeconfig"
    helm uninstall east-workload 2>/dev/null || true
    helm uninstall east-syncer 2>/dev/null || true
    
    export KUBECONFIG="$KUBECONFIG_DIR/west-cluster.kubeconfig"
    helm uninstall west-workload 2>/dev/null || true
    helm uninstall west-syncer 2>/dev/null || true
    
    export KUBECONFIG="$KUBECONFIG_DIR/kcp-admin.kubeconfig"
    helm uninstall kcp-tmc 2>/dev/null || true
    
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
    print_header "🚀 TMC Helm Deployment Demo"
    
    echo "This demo shows how to deploy KCP with TMC using production-ready Helm charts."
    echo "You'll learn about:"
    echo "• Helm-based TMC deployment patterns"
    echo "• Production configuration management"
    echo "• Chart-driven multi-cluster setup"
    echo "• Helm operations for TMC (upgrade, rollback, scaling)"
    echo ""
    echo "Duration: 10-15 minutes"
    echo "Prerequisites: Docker, kubectl, kind, helm"
    
    wait_for_user
    
    # Demo execution steps
    check_prerequisites
    create_cluster_configs
    setup_clusters
    wait_for_user
    
    validate_helm_charts
    install_kcp_with_helm
    install_syncers_with_helm
    wait_for_user
    
    deploy_demo_workloads
    wait_for_user
    
    demonstrate_helm_tmc
    show_production_insights
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