package plugin

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

var output = `
        ---
        apiVersion: v1
        kind: Namespace
        metadata:
          name: kcp-syncer-35280090ff2bf8c375b22b8f813c74dfa78d3a5041137c7941565b30
        ---
        apiVersion: v1
        kind: ServiceAccount
        metadata:
          name: kcp-syncer
          Namespace:  kcp-syncer-35280090ff2bf8c375b22b8f813c74dfa78d3a5041137c7941565b30
        ---
        apiVersion: rbac.authorization.k8s.io/v1
        kind: ClusterRole
        metadata:
          name: kcp-syncer-35280090ff2bf8c375b22b8f813c74dfa78d3a5041137c7941565b30
        rules:
        - apiGroups:
          - "*"
          resources:
          - "*"
          verbs:
          - "*"
        ---
        apiVersion: rbac.authorization.k8s.io/v1
        kind: ClusterRoleBinding
        metadata:
          name: kcp-syncer-35280090ff2bf8c375b22b8f813c74dfa78d3a5041137c7941565b30
        roleRef:
          apiGroup: rbac.authorization.k8s.io
          kind: ClusterRole
          name: kcp-syncer-35280090ff2bf8c375b22b8f813c74dfa78d3a5041137c7941565b30
        subjects:
        - kind: ServiceAccount
          name: kcp-syncer
          Namespace:  kcp-syncer-35280090ff2bf8c375b22b8f813c74dfa78d3a5041137c7941565b30
        ---
        apiVersion: v1
        kind: Secret
        metadata:
          name: kcp-syncer
          Namespace:  kcp-syncer-35280090ff2bf8c375b22b8f813c74dfa78d3a5041137c7941565b30
        data:
            
        ---
        apiVersion: apps/v1
        kind: Deployment
        metadata:
          name: kcp-syncer
          Namespace:  kcp-syncer-35280090ff2bf8c375b22b8f813c74dfa78d3a5041137c7941565b30
        spec:
          replicas: 1
          strategy:
            type: Recreate
          selector:
            matchLabels:
              app: kcp-syncer
          template:
            metadata:
              labels:
                app: kcp-syncer
            spec:
              containers:
              - name: kcp-syncer
                command:
                - /ko-app/syncer
                args:
                - --from-kubeconfig=/kcp/kubeconfig
                - --workload-cluster-name=workload-cluster-name
                - --from-cluster=from-cluster
        - --sync-resources=resource1
        - --sync-resources=resource2
                Image: image
                imagePullPolicy: IfNotPresent
                terminationMessagePolicy: FallbackToLogsOnError
                volumeMounts:
                - name: kcp-config
                  mountPath: /kcp/
                  readOnly: true
                env:
                - name: SYNCER_NAMESPACE
                  valueFrom:
                    fieldRef:
                      fieldPath: metadata.Namespace
              serviceAccountName: kcp-syncer
              volumes:
                - name: kcp-config
                  secret:
                    secretName: kcp-syncer
                    optional: false
`

func TestNewSyncerYAML(t *testing.T) {
	results, err := renderPClusterResources(templateInput{
		syncerConfigArgs: syncerConfigArgs{
			Token:  "token",
			CACert: "ca-cert",
		},
		FromCluster:     "from-cluster",
		WorkloadCluster: "workload-cluster-name",
		Image:           "image",
		ResourcesToSync: []string{"resource1", "resource2"},
	})

	if err != nil {
		t.Errorf("renderPClusterResources returned an error: %v", err)
	}

	t.Logf("%s", results)
}

func TestGetClusterRoles(t *testing.T) {
	testCases := []struct {
		name        string
		input       []string
		expected    []clusterRole
		expectError bool
	}{
		{
			name: "no cluster roles",
		},
		{
			name: "no version resourceToSync",
			input: []string{
				"foo",
			},
			expectError: true,
		},
		{
			name: "core type",
			input: []string{
				"services/v1",
			},
			expected: []clusterRole{
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
				"deployments.apps/v1",
			},
			expected: []clusterRole{
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
				"deployments.apps/v1",
				"services/v1",
				"secrets/v1",
			},
			expected: []clusterRole{
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
			actual, err := getClusterRoles(tc.input)
			if tc.expectError && err == nil {
				t.Errorf("expected error but got none")
			}
			if !tc.expectError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			sortClusterRoles(actual)
			sortClusterRoles(tc.expected)
			if len(tc.input) == 0 {
				if len(actual) != 0 {
					t.Errorf("expected no cluster roles, got %v", actual)
				}
			} else if !reflect.DeepEqual(actual, tc.expected) {
				t.Errorf("expected %v, got %v", tc.expected, actual)
			}
		})
	}
}

// sortClusterRoles sorts cluster roles by APIGroup, then by Resources
func sortClusterRoles(roles []clusterRole) {
	sort.Slice(roles, func(i, j int) bool {
		if roles[i].APIGroup == roles[j].APIGroup {
			return strings.Join(roles[i].Resources, ",") < strings.Join(roles[j].Resources, ",")
		}
		return roles[i].APIGroup < roles[j].APIGroup
	})
}
