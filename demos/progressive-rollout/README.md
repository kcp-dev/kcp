# TMC Progressive Rollout Demo

This demo showcases **TMC (Transparent Multi-Cluster) progressive rollout capabilities** with canary deployments, blue-green strategies, and automated rollback across multiple Kubernetes clusters. It demonstrates how to achieve safe, controlled application deployments with comprehensive monitoring and automatic failure detection.

## 🎯 What This Demo Shows

- **Canary Deployments**: Safe testing of new versions with limited traffic exposure
- **Progressive Promotion**: Automatic promotion through environment tiers
- **Blue-Green Production**: Zero-downtime production deployments
- **Automated Rollback**: Instant rollback on detection of critical issues
- **Health Gate Evaluation**: Policy-driven promotion criteria
- **Multi-Environment Coordination**: TMC-powered deployment orchestration

## 🏗️ Architecture Overview

```
┌─────────────────────┐    ┌─────────────────────┐    ┌─────────────────────┐    ┌─────────────────────┐
│   KCP Host Cluster  │    │   Canary Cluster    │    │  Staging Cluster    │    │ Production Cluster  │
│                     │    │                     │    │                     │    │                     │
│ ┌─────────────────┐ │    │ ┌─────────────────┐ │    │ ┌─────────────────┐ │    │ ┌─────────────────┐ │
│ │ Rollout         │ │◄──►│ │  v2.0 (5% → 100%)│ │    │ │  v2.0 (Rolling) │ │    │ │ v2.0 (Blue-Green│ │
│ │ Controller      │ │    │ │  Health Monitoring│ │    │ │  Integration    │ │    │ │  Zero Downtime) │ │
│ │                 │ │    │ │  Metrics Analysis │ │    │ │  Tests          │ │    │ │  Traffic Switch │ │
│ └─────────────────┘ │    │ └─────────────────┘ │    │ └─────────────────┘ │    │ └─────────────────┘ │
│                     │    │                     │    │                     │    │                     │
│ • Promotion Gates   │    │ • Error Rate: 0.01% │    │ • All Tests: ✅     │    │ • Response: 85ms    │
│ • Health Monitoring │    │ • Response: 95ms    │    │ • Performance: ✅   │    │ • Success: 100%     │
│ • Rollback Triggers │    │ • Success: 99.99%   │    │ • Security: ✅      │    │ • Error Rate: 0.00% │
└─────────────────────┘    └─────────────────────┘    └─────────────────────┘    └─────────────────────┘
```

## 🚀 Quick Start

### Prerequisites

- Docker and Kind installed
- kubectl configured
- At least 8GB RAM available
- Ports 42443, 42444, 42445, 42446 available

### Run the Demo

```bash
# Clone and navigate to demo
cd demos/progressive-rollout

# Run the complete demo
./run-demo.sh

# Or run with environment options
DEMO_PAUSE_STEPS=false ./run-demo.sh  # Run without pauses
DEMO_DEBUG=true ./run-demo.sh         # Enable debug output
```

### Monitor Rollout Progress

```bash
# Real-time rollout status
./scripts/show-rollout-status.sh

# Live canary monitoring (during demo)
./scripts/monitor-canary.sh

# Integration test results (during demo)
./scripts/run-integration-tests.sh
```

## 📊 Demo Flow

### Phase 1: Infrastructure Setup
1. **Multi-Cluster Creation**: KCP host + Canary/Staging/Production clusters
2. **Rollout System**: TMC rollout controller and monitoring deployment
3. **Baseline Deployment**: Application v1.0 across all environments

### Phase 2: Canary Rollout (v1.0 → v2.0)
1. **Canary Deployment**: v2.0 deployed to canary with 5% traffic
2. **Health Monitoring**: Real-time metrics collection and analysis
3. **Gate Evaluation**: Automated promotion criteria validation
4. **Traffic Ramping**: Gradual increase to 100% canary traffic

### Phase 3: Staging Promotion
1. **Staging Deployment**: v2.0 promoted to staging environment
2. **Integration Testing**: Comprehensive test suite execution
3. **Performance Validation**: Load testing and regression analysis
4. **Security Scanning**: Vulnerability assessment and validation

### Phase 4: Production Rollout
1. **Blue-Green Deployment**: Zero-downtime production strategy
2. **Traffic Switching**: Gradual traffic migration (10% → 100%)
3. **Health Validation**: Continuous monitoring during rollout
4. **Rollout Completion**: Full production deployment success

### Phase 5: Rollback Demonstration
1. **Problematic Version**: Deploy v2.1 with critical issues
2. **Issue Detection**: Automated monitoring detects failures
3. **Rollback Trigger**: Automatic rollback to last known good version
4. **Recovery Validation**: System health restoration verification

## 🎯 Rollout Strategies

### Canary Deployment Strategy
- **Traffic Splitting**: 5% initial traffic exposure
- **Health Monitoring**: Real-time error rate and latency tracking
- **Automatic Promotion**: Based on success criteria thresholds
- **Quick Rollback**: Instant revert on detection of issues

```yaml
# Canary Configuration
rollout_config:
  traffic_split: 5
  error_threshold: 1.0
  response_time_threshold: 200
  promotion_criteria:
    min_success_rate: 99.5
    max_error_rate: 0.5
    min_duration_minutes: 10
```

### Blue-Green Production Strategy
- **Zero Downtime**: Complete environment switching
- **Validation Period**: Health checks before traffic switch
- **Instant Rollback**: Switch back to blue environment if needed
- **Resource Efficiency**: Temporary dual environment during switch

### Progressive Promotion Gates
- **Canary → Staging**: Error rate < 1%, Response time < 200ms
- **Staging → Production**: All tests pass, Manual approval
- **Production**: Blue-green with health validation

## 📈 Monitoring & Health Gates

### Key Metrics Tracked
```
Application Health Metrics:
├── Performance
│   ├── Response Time: <200ms (canary/staging), <100ms (prod)
│   ├── Throughput: req/min capacity
│   └── Resource Usage: CPU/Memory utilization
├── Reliability  
│   ├── Error Rate: <1% (canary/staging), <0.1% (prod)
│   ├── Success Rate: >99% (canary/staging), >99.9% (prod)
│   └── Health Check Status: All endpoints healthy
└── Infrastructure
    ├── Pod Availability: Ready replicas
    ├── Service Connectivity: Network reachability
    └── Resource Limits: Within allocated quotas
```

### Automated Health Gates
1. **Canary Gate**: 
   - ✅ Error rate < 1.0% (actual: 0.01%)
   - ✅ Response time < 200ms (actual: 95ms)
   - ✅ Success rate > 99% (actual: 99.99%)
   - ✅ Minimum duration 10min (completed)

2. **Staging Gate**:
   - ✅ Integration tests passed (100%)
   - ✅ Security scan passed
   - ✅ Performance tests passed
   - ✅ Manual approval received

3. **Production Gate**:
   - ✅ Blue-green deployment healthy
   - ✅ Traffic switching successful
   - ✅ All health checks passing
   - ✅ No error spike detected

### Rollback Triggers
- **Error Rate Spike**: >1% sustained for >2 minutes
- **Response Time Degradation**: >500ms average for >5 minutes
- **Health Check Failures**: >20% failure rate
- **Resource Exhaustion**: Memory/CPU limits exceeded

## 🔄 Rollback Capabilities

### Automatic Rollback Scenarios
```bash
# Problematic version deployment (v2.1)
Error Rate: 15.3% (threshold: 1%)
Response Time: 2400ms (threshold: 200ms)
Health Checks: 23% failures
→ AUTOMATIC ROLLBACK TRIGGERED

# Rollback execution
1. Traffic immediately switched to v2.0 (last known good)
2. Problematic pods terminated
3. Health validation confirms recovery
4. Alert notifications sent to operations team
```

### Rollback Readiness
- **Previous Version Available**: v1.0.0/v2.0.0 images ready
- **Database Compatibility**: Backward compatible migrations
- **Configuration Rollback**: Previous configs preserved
- **Automated Procedures**: Rollback scripts tested and verified

## 🧪 Interactive Features

### Version Comparison
```bash
# View application versions across environments
kubectl get deployments --all-namespaces -l app=webapp

# Check version-specific configurations
kubectl get configmaps -l version=v2.0.0
kubectl get configmaps -l version=v2.1.0
```

### Traffic Distribution Testing
```bash
# Monitor traffic split during canary
kubectl get services -l app=webapp -o wide

# View rollout annotations
kubectl get deployments webapp-v2 -o yaml | grep rollout
```

### Health Validation
```bash
# Check application health endpoints
curl http://127.0.0.1:30981/health  # Canary
curl http://127.0.0.1:30982/health  # Staging  
curl http://127.0.0.1:30988/health  # Production

# Monitor pod readiness
kubectl get pods -l app=webapp --all-namespaces
```

## 📊 Feature Flag Management

### Version-Specific Features
```json
// v1.0.0 Features
{
  "legacy-ui": true,
  "new-api": false,
  "advanced-analytics": false,
  "experimental-features": false
}

// v2.0.0 Features  
{
  "legacy-ui": false,
  "new-api": true,
  "advanced-analytics": true,
  "real-time-updates": true,
  "experimental-features": false
}

// v2.1.0 Features (Problematic)
{
  "experimental-features": true,
  "unstable-features": true,
  "beta-features": true
}
```

### Feature Flag Benefits
- **Risk Reduction**: Enable features gradually
- **A/B Testing**: Compare feature performance
- **Quick Rollback**: Disable problematic features instantly
- **User Segmentation**: Target specific user groups

## 🔧 Testing Scenarios

### Successful Rollout Testing
1. **Baseline Performance**: Validate v1.0 metrics
2. **Canary Health**: Monitor v2.0 in canary environment
3. **Staging Validation**: Execute full test suite
4. **Production Deployment**: Verify zero-downtime switch
5. **Post-Deployment**: Confirm improved performance

### Rollback Scenario Testing  
1. **Deploy Problematic Version**: v2.1 with simulated issues
2. **Monitor Health Degradation**: Watch metrics exceed thresholds
3. **Automatic Rollback**: Verify immediate revert to v2.0
4. **Recovery Validation**: Confirm system health restoration
5. **Alert Verification**: Check notification delivery

### Load Testing Integration
```bash
# Simulate load during rollout
while true; do
  curl -s http://127.0.0.1:30988/ > /dev/null
  sleep 0.1
done

# Monitor performance during traffic switching
watch kubectl top pods -l app=webapp
```

## 📚 Production Considerations

### Scalability Guidelines
- **Cluster Capacity**: Plan for temporary dual deployments
- **Network Bandwidth**: Account for increased traffic during switch
- **Monitoring Overhead**: Real-time metrics collection impact
- **Storage Requirements**: Multiple version artifacts

### Security Considerations
- **Image Scanning**: Automated vulnerability assessment
- **Secret Management**: Version-specific configuration secrets
- **Network Policies**: Maintain security boundaries during rollout
- **Access Control**: Rollout operation permissions

### High Availability
- **Multi-Region**: Deploy across availability zones
- **Database HA**: Ensure database compatibility during rollout
- **Monitoring Redundancy**: Multiple monitoring systems
- **Rollback Speed**: Sub-minute rollback capabilities

## 🔧 Troubleshooting

### Common Issues

#### Rollout Stuck in Canary
```bash
# Check canary health metrics
kubectl logs -l app=rollout-controller -n rollout-system

# Verify promotion gate criteria
./scripts/show-rollout-status.sh | grep -A 10 "Gate Status"

# Manual promotion (if needed)
kubectl annotate deployment webapp-v2 rollout.tmc.io/promote=true
```

#### Rollback Not Triggering
```bash
# Check rollback trigger thresholds
kubectl describe deployment webapp-v2 | grep -A 5 annotations

# Verify health monitoring
kubectl logs -l app=rollout-metrics-collector -n rollout-system

# Manual rollback
kubectl apply -f manifests/rollback-canary.yaml
```

#### Traffic Not Switching
```bash
# Check service configurations
kubectl get services -l app=webapp -o yaml

# Verify ingress/load balancer setup
kubectl get ingress --all-namespaces

# Test connectivity
kubectl exec -it <pod> -- curl webapp-service
```

### Debug Mode
Enable comprehensive logging for troubleshooting:
```bash
DEMO_DEBUG=true ./run-demo.sh
```

## 🧹 Cleanup

Remove all demo resources:
```bash
# Interactive cleanup (with confirmations)
./cleanup.sh

# Force cleanup (no confirmations)  
./cleanup.sh --force
```

The cleanup script removes:
- All Kind clusters
- Docker containers and networks
- Temporary files and logs
- Kubeconfig files

Application configurations are preserved in manifests for future use.

## 📖 Related Documentation

- [TMC Architecture Overview](../README.md)
- [Policy Enforcement Demo](../policy-enforcement/README.md)
- [Multi-Tenant Demo](../multi-tenant/README.md)
- [GitOps Integration Demo](../gitops-integration/README.md)

## 🤝 Contributing

To extend this demo:

1. **Add Rollout Strategies**: Create new deployment patterns in `manifests/`
2. **Extend Monitoring**: Add metrics to `scripts/monitor-rollout.sh`
3. **New Health Gates**: Add validation criteria to rollout controller
4. **Documentation**: Update this README with new features

## 📝 License

This demo is part of the TMC project and follows the same licensing terms.

---

**Summary**: This progressive rollout demo showcases TMC's advanced deployment capabilities, demonstrating how to safely roll out applications across multiple environments with comprehensive monitoring, automated health gates, and instant rollback capabilities. Perfect for understanding production-grade deployment strategies in multi-cluster environments.