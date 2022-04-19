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

	"github.com/stretchr/testify/require"
)

func TestNewSyncerYAML(t *testing.T) {
	expectedYAML := `---
apiVersion: v1
kind: Namespace
metadata:
  name: kcpsync25e6e3ce5be10b16411448aec95b6b6d695a1daa5120732019531d8d
  labels:
    workload.kcp.io/logical-cluster: root_default_foo
    workload.kcp.io/workload-cluster: workload-cluster-name
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: kcp-syncer
  namespace:  kcpsync25e6e3ce5be10b16411448aec95b6b6d695a1daa5120732019531d8d
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: kcpsync25e6e3ce5be10b16411448aec95b6b6d695a1daa5120732019531d8d
rules:
- apiGroups:
  - ""
  resources:
  - namespaces
  verbs:
  - "create"
  - "list"
  - "watch"
- apiGroups:
  - "apiextensions.k8s.io"
  resources:
  - customresourcedefinitions
  verbs:
  - "get"
  - "watch"
  - "list"
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
  name: kcpsync25e6e3ce5be10b16411448aec95b6b6d695a1daa5120732019531d8d
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: kcpsync25e6e3ce5be10b16411448aec95b6b6d695a1daa5120732019531d8d
subjects:
- kind: ServiceAccount
  name: kcp-syncer
  namespace:  kcpsync25e6e3ce5be10b16411448aec95b6b6d695a1daa5120732019531d8d
---
apiVersion: v1
kind: Secret
metadata:
  name: kcp-syncer-config
  namespace:  kcpsync25e6e3ce5be10b16411448aec95b6b6d695a1daa5120732019531d8d
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
  name: kcp-syncer
  namespace:  kcpsync25e6e3ce5be10b16411448aec95b6b6d695a1daa5120732019531d8d
spec:
  replicas: 1
  strategy:
    type: Recreate
  selector:
    matchLabels:
      app: kcpsync25e6e3ce5be10b16411448aec95b6b6d695a1daa5120732019531d8d
  template:
    metadata:
      labels:
        app: kcpsync25e6e3ce5be10b16411448aec95b6b6d695a1daa5120732019531d8d
    spec:
      containers:
      - name: kcp-syncer
        command:
        - /ko-app/syncer
        args:
        - --from-kubeconfig=/kcp/kubeconfig
        - --workload-cluster-name=workload-cluster-name
        - --from-cluster=root:default:foo
        - --sync-resources=resource1
        - --sync-resources=resource2
        image: image
        imagePullPolicy: IfNotPresent
        terminationMessagePolicy: FallbackToLogsOnError
        volumeMounts:
        - name: kcp-config
          mountPath: /kcp/
          readOnly: true
      serviceAccountName: kcp-syncer
      volumes:
        - name: kcp-config
          secret:
            secretName: kcp-syncer-config
            optional: false
`
	actualYAML, err := renderSyncerResources(templateInput{
		ServerURL:       "server-url",
		Token:           "token",
		CAData:          "ca-data",
		KCPNamespace:    "kcp-namespace",
		LogicalCluster:  "root:default:foo",
		WorkloadCluster: "workload-cluster-name",
		Image:           "image",
		Replicas:        1,
		ResourcesToSync: []string{"resource1", "resource2"},
	})
	require.NoError(t, err)
	require.Equal(t, expectedYAML, string(actualYAML))
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
				require.Equal(t, tc.expected, actual)
			}
		})
	}
}
