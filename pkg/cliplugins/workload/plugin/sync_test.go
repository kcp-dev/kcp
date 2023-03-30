/*
Copyright 2022 The KCP Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package plugin

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"
)

func TestNewSyncerYAML(t *testing.T) {
	expectedYAML := `---
apiVersion: v1
kind: Namespace
metadata:
  name: kcp-syncer-sync-target-name-34b23c4k
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: kcp-syncer-sync-target-name-34b23c4k
  namespace: kcp-syncer-sync-target-name-34b23c4k
---
apiVersion: v1
kind: Secret
metadata:
  name: kcp-syncer-sync-target-name-34b23c4k-token
  namespace: kcp-syncer-sync-target-name-34b23c4k
  annotations:
    kubernetes.io/service-account.name: kcp-syncer-sync-target-name-34b23c4k
type: kubernetes.io/service-account-token
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: kcp-syncer-sync-target-name-34b23c4k
rules:
- apiGroups:
  - ""
  resources:
  - namespaces
  verbs:
  - "create"
  - "get"
  - "list"
  - "watch"
  - "delete"
- apiGroups:
  - ""
  resources:
  - endpoints
  verbs:
  - "get"
- apiGroups:
  - "apiextensions.k8s.io"
  resources:
  - customresourcedefinitions
  verbs:
  - "get"
  - "watch"
  - "list"
- apiGroups:
  - "networking.k8s.io"
  resources:
  - networkpolicies
  verbs:
  - "create"
  - "list"
  - "watch"
  - "get"
  - "update"
- apiGroups:
  - ""
  resources:
  - resource1
  - resource2
  verbs:
  - "*"
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: kcp-syncer-sync-target-name-34b23c4k
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: kcp-syncer-sync-target-name-34b23c4k
subjects:
- kind: ServiceAccount
  name: kcp-syncer-sync-target-name-34b23c4k
  namespace: kcp-syncer-sync-target-name-34b23c4k
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: kcp-dns-sync-target-name-34b23c4k
  namespace: kcp-syncer-sync-target-name-34b23c4k
rules:
- apiGroups:
  - ""
  resources:
  - serviceaccounts
  - services
  verbs:
  - "create"
  - "get"
  - "list"
  - "update"
  - "delete"
  - "watch"
- apiGroups:
  - ""
  resources:
  - endpoints
  verbs:
  - "get"
  - "list"
  - "watch"
- apiGroups:
  - "apps"
  resources:
  - deployments
  verbs:
  - "create"
  - "get"
  - "list"
  - "update"
  - "delete"
  - "watch"
- apiGroups:
  - "rbac.authorization.k8s.io"
  resources:
  - roles
  - rolebindings
  verbs:
  - "create"
  - "get"
  - "list"
  - "update"
  - "delete"
  - "watch"
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: kcp-dns-sync-target-name-34b23c4k
  namespace: kcp-syncer-sync-target-name-34b23c4k
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: kcp-dns-sync-target-name-34b23c4k
subjects:
  - kind: ServiceAccount
    name: kcp-syncer-sync-target-name-34b23c4k
    namespace: kcp-syncer-sync-target-name-34b23c4k
---
apiVersion: v1
kind: Secret
metadata:
  name: kcp-syncer-sync-target-name-34b23c4k
  namespace: kcp-syncer-sync-target-name-34b23c4k
stringData:
  kubeconfig: |
    apiVersion: v1
    kind: Config
    clusters:
    - name: default-cluster
      cluster:
        certificate-authority-data: ca-data
        server: server-url
    contexts:
    - name: default-context
      context:
        cluster: default-cluster
        namespace: kcp-namespace
        user: default-user
    current-context: default-context
    users:
    - name: default-user
      user:
        token: token
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: kcp-syncer-sync-target-name-34b23c4k
  namespace: kcp-syncer-sync-target-name-34b23c4k
spec:
  replicas: 1
  strategy:
    type: Recreate
  selector:
    matchLabels:
      app: kcp-syncer-sync-target-name-34b23c4k
  template:
    metadata:
      labels:
        app: kcp-syncer-sync-target-name-34b23c4k
    spec:
      containers:
      - name: kcp-syncer
        command:
        - /ko-app/syncer
        args:
        - --from-kubeconfig=/kcp/kubeconfig
        - --sync-target-name=sync-target-name
        - --sync-target-uid=sync-target-uid
        - --from-cluster=root:default:foo
        - --api-import-poll-interval=1m
        - --downstream-namespace-clean-delay=2s
        - --resources=resource1
        - --resources=resource2
        - --qps=123.4
        - --burst=456
        - --dns-image=image
        env:
        - name: NAMESPACE
          valueFrom:
            fieldRef:
              fieldPath: metadata.namespace
        image: image
        imagePullPolicy: IfNotPresent
        terminationMessagePolicy: FallbackToLogsOnError
        volumeMounts:
        - name: kcp-config
          mountPath: /kcp/
          readOnly: true
      serviceAccountName: kcp-syncer-sync-target-name-34b23c4k
      volumes:
        - name: kcp-config
          secret:
            secretName: kcp-syncer-sync-target-name-34b23c4k
            optional: false
`

	actualYAML, err := renderSyncerResources(templateInput{
		ServerURL:                           "server-url",
		Token:                               "token",
		CAData:                              "ca-data",
		KCPNamespace:                        "kcp-namespace",
		Namespace:                           "kcp-syncer-sync-target-name-34b23c4k",
		SyncTargetPath:                      "root:default:foo",
		SyncTarget:                          "sync-target-name",
		SyncTargetUID:                       "sync-target-uid",
		Image:                               "image",
		Replicas:                            1,
		ResourcesToSync:                     []string{"resource1", "resource2"},
		APIImportPollIntervalString:         "1m",
		DownstreamNamespaceCleanDelayString: "2s",
		QPS:                                 123.4,
		Burst:                               456,
	}, "kcp-syncer-sync-target-name-34b23c4k", []string{"resource1", "resource2"})
	require.NoError(t, err)
	require.Empty(t, cmp.Diff(expectedYAML, string(actualYAML)))
}

func TestNewSyncerYAMLWithFeatureGates(t *testing.T) {
	expectedYAML := `---
apiVersion: v1
kind: Namespace
metadata:
  name: kcp-syncer-sync-target-name-34b23c4k
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: kcp-syncer-sync-target-name-34b23c4k
  namespace: kcp-syncer-sync-target-name-34b23c4k
---
apiVersion: v1
kind: Secret
metadata:
  name: kcp-syncer-sync-target-name-34b23c4k-token
  namespace: kcp-syncer-sync-target-name-34b23c4k
  annotations:
    kubernetes.io/service-account.name: kcp-syncer-sync-target-name-34b23c4k
type: kubernetes.io/service-account-token
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: kcp-syncer-sync-target-name-34b23c4k
rules:
- apiGroups:
  - ""
  resources:
  - namespaces
  verbs:
  - "create"
  - "get"
  - "list"
  - "watch"
  - "delete"
- apiGroups:
  - ""
  resources:
  - endpoints
  verbs:
  - "get"
- apiGroups:
  - "apiextensions.k8s.io"
  resources:
  - customresourcedefinitions
  verbs:
  - "get"
  - "watch"
  - "list"
- apiGroups:
  - "networking.k8s.io"
  resources:
  - networkpolicies
  verbs:
  - "create"
  - "list"
  - "watch"
  - "get"
  - "update"
- apiGroups:
  - ""
  resources:
  - resource1
  - resource2
  verbs:
  - "*"
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: kcp-syncer-sync-target-name-34b23c4k
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: kcp-syncer-sync-target-name-34b23c4k
subjects:
- kind: ServiceAccount
  name: kcp-syncer-sync-target-name-34b23c4k
  namespace: kcp-syncer-sync-target-name-34b23c4k
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: kcp-dns-sync-target-name-34b23c4k
  namespace: kcp-syncer-sync-target-name-34b23c4k
rules:
- apiGroups:
  - ""
  resources:
  - serviceaccounts
  - services
  verbs:
  - "create"
  - "get"
  - "list"
  - "update"
  - "delete"
  - "watch"
- apiGroups:
  - ""
  resources:
  - endpoints
  verbs:
  - "get"
  - "list"
  - "watch"
- apiGroups:
  - "apps"
  resources:
  - deployments
  verbs:
  - "create"
  - "get"
  - "list"
  - "update"
  - "delete"
  - "watch"
- apiGroups:
  - "rbac.authorization.k8s.io"
  resources:
  - roles
  - rolebindings
  verbs:
  - "create"
  - "get"
  - "list"
  - "update"
  - "delete"
  - "watch"
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: kcp-dns-sync-target-name-34b23c4k
  namespace: kcp-syncer-sync-target-name-34b23c4k
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: kcp-dns-sync-target-name-34b23c4k
subjects:
  - kind: ServiceAccount
    name: kcp-syncer-sync-target-name-34b23c4k
    namespace: kcp-syncer-sync-target-name-34b23c4k
---
apiVersion: v1
kind: Secret
metadata:
  name: kcp-syncer-sync-target-name-34b23c4k
  namespace: kcp-syncer-sync-target-name-34b23c4k
stringData:
  kubeconfig: |
    apiVersion: v1
    kind: Config
    clusters:
    - name: default-cluster
      cluster:
        certificate-authority-data: ca-data
        server: server-url
    contexts:
    - name: default-context
      context:
        cluster: default-cluster
        namespace: kcp-namespace
        user: default-user
    current-context: default-context
    users:
    - name: default-user
      user:
        token: token
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: kcp-syncer-sync-target-name-34b23c4k
  namespace: kcp-syncer-sync-target-name-34b23c4k
spec:
  replicas: 1
  strategy:
    type: Recreate
  selector:
    matchLabels:
      app: kcp-syncer-sync-target-name-34b23c4k
  template:
    metadata:
      labels:
        app: kcp-syncer-sync-target-name-34b23c4k
    spec:
      containers:
      - name: kcp-syncer
        command:
        - /ko-app/syncer
        args:
        - --from-kubeconfig=/kcp/kubeconfig
        - --sync-target-name=sync-target-name
        - --sync-target-uid=sync-target-uid
        - --from-cluster=root:default:foo
        - --api-import-poll-interval=1m
        - --downstream-namespace-clean-delay=2s
        - --resources=resource1
        - --resources=resource2
        - --qps=123.4
        - --burst=456
        - --feature-gates=myfeature=true
        - --dns-image=image
        env:
        - name: NAMESPACE
          valueFrom:
            fieldRef:
              fieldPath: metadata.namespace
        image: image
        imagePullPolicy: IfNotPresent
        terminationMessagePolicy: FallbackToLogsOnError
        volumeMounts:
        - name: kcp-config
          mountPath: /kcp/
          readOnly: true
      serviceAccountName: kcp-syncer-sync-target-name-34b23c4k
      volumes:
        - name: kcp-config
          secret:
            secretName: kcp-syncer-sync-target-name-34b23c4k
            optional: false
`
	actualYAML, err := renderSyncerResources(templateInput{
		ServerURL:                           "server-url",
		Token:                               "token",
		CAData:                              "ca-data",
		KCPNamespace:                        "kcp-namespace",
		Namespace:                           "kcp-syncer-sync-target-name-34b23c4k",
		SyncTargetPath:                      "root:default:foo",
		SyncTarget:                          "sync-target-name",
		SyncTargetUID:                       "sync-target-uid",
		Image:                               "image",
		Replicas:                            1,
		ResourcesToSync:                     []string{"resource1", "resource2"},
		QPS:                                 123.4,
		Burst:                               456,
		APIImportPollIntervalString:         "1m",
		DownstreamNamespaceCleanDelayString: "2s",
		FeatureGatesString:                  "myfeature=true",
	}, "kcp-syncer-sync-target-name-34b23c4k", []string{"resource1", "resource2"})
	require.NoError(t, err)
	require.Empty(t, cmp.Diff(expectedYAML, string(actualYAML)))
}

func TestGetGroupMappings(t *testing.T) {
	testCases := []struct {
		name     string
		input    []string
		expected []groupMapping
	}{
		{
			name: "no group mappings",
		},
		{
			name: "core type",
			input: []string{
				"services",
			},
			expected: []groupMapping{
				{
					APIGroup: "",
					Resources: []string{
						"services",
					},
				},
			},
		},
		{
			name: "type with group",
			input: []string{
				"deployments.apps",
			},
			expected: []groupMapping{
				{
					APIGroup: "apps",
					Resources: []string{
						"deployments",
					},
				},
			},
		},
		{
			name: "multiple types",
			input: []string{
				"deployments.apps",
				"services",
				"secrets",
			},
			expected: []groupMapping{
				{
					APIGroup: "",
					Resources: []string{
						"services",
						"secrets",
					},
				},
				{
					APIGroup: "apps",
					Resources: []string{
						"deployments",
					},
				},
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			actual := getGroupMappings(tc.input)
			if len(tc.input) == 0 {
				require.Empty(t, actual)
			} else {
				require.Empty(t, cmp.Diff(tc.expected, actual))
			}
		})
	}
}
