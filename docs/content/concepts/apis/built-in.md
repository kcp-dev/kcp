# Kubernetes API Endpoints used by kcp

kcp includes some, but not all, of the Kubernetes APIs.

kcp does not make us of Kubernetes API endpoints that are concerned with orchestrating clusters and their workloads. Clusters dedicated to workload orchestration have these resources but kcp does not.

## Kubernetes resources used by kcp

kcp makes use of these API Endpoints and resources to provide kcp features and indoing so leverages the Kubernetes Event Loop to manage non-cluster resources that can them be handles using conventions and tooling from the Kubernetes eco-system.

### (core) v1
- Namespaces
- ConfigMaps
- Secrets
- Events
- LimitRanges
- ResourceQuotas
- ServiceAccounts

### admissionregistration.k8s.io/v1
- MutatingWebhookConfigurations
- ValidatingWebhookConfigurations
- ValidatingAdmissionPolicies
- ValidatingAdmissionPolicyBindings

### apiextensions.k8s.io/v1
- CustomResourceDefinitions

### authentication.k8s.io/v1
- TokenReviews

### authorization.k8s.io/v1
- LocalSubjectAccessReviews
- SelfSubjectAccessReviews
- SelfSubjectRulesReviews
- SubjectAccessReviews

### certificates.k8s.io/v1
- CertificateSigningRequests

### coordination.k8s.io/v1
- Leases

### events.k8s.io/v1
- Events

### flowcontrol.apiserver.k8s.io/v1beta1 (temporarily removed)
- FlowSchemas
- PriorityLevelConfigurations

### rbac.authorization.k8s.io/v1
- ClusterRoleBindings
- ClusterRoles
- RoleBindings
- Roles

