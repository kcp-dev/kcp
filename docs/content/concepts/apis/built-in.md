# Built-in APIs

kcp includes some, but not all, of the APIs you are likely familiar with from Kubernetes:

## (core) v1
- Namespaces
- ConfigMaps
- Secrets
- Events
- LimitRanges
- ResourceQuotas
- ServiceAccounts

## admissionregistration.k8s.io/v1
- MutatingWebhookConfigurations
- ValidatingWebhookConfigurations
- ValidatingAdmissionPolicies
- ValidatingAdmissionPolicyBindings

## apiextensions.k8s.io/v1
- CustomResourceDefinitions

## authentication.k8s.io/v1
- TokenReviews

## authorization.k8s.io/v1
- LocalSubjectAccessReviews
- SelfSubjectAccessReviews
- SelfSubjectRulesReviews
- SubjectAccessReviews

## certificates.k8s.io/v1
- CertificateSigningRequests

## coordination.k8s.io/v1
- Leases

## events.k8s.io/v1
- Events

## flowcontrol.apiserver.k8s.io/v1beta1 (temporarily removed)
- FlowSchemas
- PriorityLevelConfigurations

## rbac.authorization.k8s.io/v1
- ClusterRoleBindings
- ClusterRoles
- RoleBindings
- Roles

Notably, workload-related APIs (Pods, ReplicaSets, Deployments, Jobs, CronJobs, StatefulSets), cluster-related APIs (
Nodes), storage-related APIs (PersistentVolumes, PersistentVolumeClaims) are all missing - kcp does not include these,
and it instead relies on workload clusters to provide this functionality.
