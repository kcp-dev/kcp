/*
Copyright 2021 The KCP Authors.

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

package registry

import (
	"context"
	"encoding/base64"
	"testing"

	"github.com/kcp-dev/apimachinery/pkg/logicalcluster"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kuser "k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/client-go/kubernetes/fake"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	tenancyv1fake "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/fake"
	workspaceauth "github.com/kcp-dev/kcp/pkg/virtual/workspaces/authorization"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/third_party/conditions/apis/conditions/v1alpha1"
)

var (
	shardKubeConfigContent string = `
kind: Config
apiVersion: v1
clusters:
- name: admin
  cluster:
    certificate-authority-data: ` + base64.StdEncoding.EncodeToString([]byte("THE_RIGHT_CA_DATA")) + `
    server: ADMIN_SERVER
    tls-server-name: THE_RIGHT_TLS_SERVER_NAME
users:
- name: loopback
  user:
    token: loopback-token
contexts:
- name: admin
  context:
    cluster: admin
    user: loopback
current-context: admin
`

	shardKubeConfigContentInvalidCADataBase64 string = `
kind: Config
apiVersion: v1
clusters:
- name: admin
  cluster:
    certificate-authority-data: INVALID_VALUE
    server: ADMIN_SERVER
    tls-server-name: THE_RIGHT_TLS_SERVER_NAME
users:
- name: loopback
  user:
    token: loopback-token
contexts:
- name: admin
  context:
    cluster: admin
    user: loopback
current-context: admin
`

	shardKubeConfigContentWithoutContext string = `
kind: Config
apiVersion: v1
clusters:
- name: admin
  cluster:
    certificate-authority-data: ` + base64.StdEncoding.EncodeToString([]byte("THE_RIGHT_CA_DATA")) + `
    server: ADMIN_SERVER
    tls-server-name: THE_RIGHT_TLS_SERVER_NAME
users:
- name: loopback
  user:
    token: loopback-token
contexts:
- name: admin
  context:
    cluster: admin
    user: loopback
current-context: nonexistent
`

	shardKubeConfigContentInvalid string = `
kind: Config
invalid
  text
`
)

func expectedWorkspaceKubeconfigContent(workspaceScope string) string {
	contextName := workspaceScope + "/foo"
	return `
kind: Config
apiVersion: v1
clusters:
- name: ` + contextName + `
  cluster:
    certificate-authority-data: ` + base64.StdEncoding.EncodeToString([]byte("THE_RIGHT_CA_DATA")) + `
    server: THE_RIGHT_SERVER_URL
    tls-server-name: THE_RIGHT_TLS_SERVER_NAME
contexts:
- name: ` + contextName + `
  context:
    cluster: ` + contextName + `
    user: ''
current-context: ` + contextName + `
users:
preferences: {}
`
}

type mockSubjectLocator struct {
	// "verb/resource/[subresource]" -> "name" -> subjects
	subjects map[string]map[string][]rbacv1.Subject
}

func attrKey(attributes authorizer.Attributes) string {
	key := attributes.GetVerb() + "/" + attributes.GetAPIGroup() + "/" + attributes.GetAPIVersion() + "/" + attributes.GetResource()
	if attributes.GetSubresource() != "" {
		key += "/" + attributes.GetSubresource()
	}
	return key
}

func (m *mockSubjectLocator) AllowedSubjects(attributes authorizer.Attributes) ([]rbacv1.Subject, error) {
	if subjects, ok := m.subjects[attrKey(attributes)]; ok {
		return subjects[attributes.GetName()], nil
	}
	return nil, nil
}

func rbacUser(name string) rbacv1.Subject {
	return rbacv1.Subject{
		APIGroup: rbacv1.GroupName,
		Kind:     rbacv1.UserKind,
		Name:     name,
	}
}

func rbacUsers(names ...string) []rbacv1.Subject {
	var subjects []rbacv1.Subject

	for _, name := range names {
		subjects = append(subjects, rbacUser(name))
	}

	return subjects
}

func rbacGroup(name string) rbacv1.Subject {
	return rbacv1.Subject{
		APIGroup: rbacv1.GroupName,
		Kind:     rbacv1.GroupKind,
		Name:     name,
	}
}

func rbacGroups(names ...string) []rbacv1.Subject {
	var subjects []rbacv1.Subject

	for _, name := range names {
		subjects = append(subjects, rbacGroup(name))
	}

	return subjects
}

func TestKubeconfigPersonalWorkspaceWithPrettyName(t *testing.T) {
	user := &kuser.DefaultInfo{
		Name:   "test-user",
		UID:    "test-uid",
		Groups: []string{"test-group"},
	}
	test := TestDescription{
		TestData: TestData{
			user:    user,
			scope:   "personal",
			orgName: logicalcluster.New("root:orgName"),
			rootReviewer: workspaceauth.NewReviewer(&mockSubjectLocator{
				subjects: map[string]map[string][]rbacv1.Subject{
					"access/tenancy.kcp.dev/v1alpha1/clusterworkspaces/content": {
						"orgName": rbacGroups("test-group"),
					},
				},
			}),
			clusterWorkspaces: []tenancyv1alpha1.ClusterWorkspace{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "foo--1", ClusterName: "root:orgName"},
					Status: tenancyv1alpha1.ClusterWorkspaceStatus{
						BaseURL: "THE_RIGHT_SERVER_URL",
						Location: tenancyv1alpha1.ClusterWorkspaceLocation{
							Current: "theOneAndOnlyShard",
						},
						Conditions: conditionsv1alpha1.Conditions{
							{
								Type:   tenancyv1alpha1.WorkspaceShardValid,
								Status: corev1.ConditionTrue,
							},
						},
					},
				},
			},
			workspaceShards: []tenancyv1alpha1.ClusterWorkspaceShard{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "theOneAndOnlyShard",
						ClusterName: "root",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceShardSpec{
						Credentials: corev1.SecretReference{
							Name:      "kubeconfig",
							Namespace: "kcp",
						},
					},
				},
			},
			secrets: []corev1.Secret{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "kubeconfig",
						Namespace:   "kcp",
						ClusterName: "root",
					},
					Data: map[string][]byte{
						"kubeconfig": []byte(shardKubeConfigContent),
					},
				},
			},
			clusterRoleBindings: []rbacv1.ClusterRoleBinding{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        getRoleBindingName(OwnerRoleType, "foo", user),
						ClusterName: "root:orgName",
						Labels: map[string]string{
							PrettyNameLabel:   "foo",
							InternalNameLabel: "foo--1",
						},
					},
					Subjects: []rbacv1.Subject{
						{
							Kind: "User",
							Name: user.Name,
						},
					},
				},
			},
		},
		apply: func(t *testing.T, storage *REST, kubeconfigSubResourceStorage *KubeconfigSubresourceREST, ctx context.Context, kubeClient *fake.Clientset, kcpClient *tenancyv1fake.Clientset, listerCheckedUsers func() []kuser.Info, testData TestData) {
			response, err := kubeconfigSubResourceStorage.Get(ctx, "foo", nil)
			require.NoError(t, err)
			require.IsType(t, KubeConfig(""), response)
			responseWorkspace := response.(KubeConfig)
			assert.YAMLEq(t, expectedWorkspaceKubeconfigContent("personal"), string(responseWorkspace))
		},
	}
	applyTest(t, test)
}

func TestKubeconfigPersonalWorkspace(t *testing.T) {
	user := &kuser.DefaultInfo{
		Name:   "test-user",
		UID:    "test-uid",
		Groups: []string{"test-group"},
	}
	test := TestDescription{
		TestData: TestData{
			user:    user,
			scope:   "personal",
			orgName: logicalcluster.New("root:orgName"),
			rootReviewer: workspaceauth.NewReviewer(&mockSubjectLocator{
				subjects: map[string]map[string][]rbacv1.Subject{
					"access/tenancy.kcp.dev/v1alpha1/clusterworkspaces/content": {
						"orgName": rbacGroups("test-group"),
					},
				},
			}),
			clusterWorkspaces: []tenancyv1alpha1.ClusterWorkspace{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "foo", ClusterName: "root:orgName"},
					Status: tenancyv1alpha1.ClusterWorkspaceStatus{
						BaseURL: "THE_RIGHT_SERVER_URL",
						Location: tenancyv1alpha1.ClusterWorkspaceLocation{
							Current: "theOneAndOnlyShard",
						},
						Conditions: conditionsv1alpha1.Conditions{
							{
								Type:   tenancyv1alpha1.WorkspaceShardValid,
								Status: corev1.ConditionTrue,
							},
						},
					},
				},
			},
			workspaceShards: []tenancyv1alpha1.ClusterWorkspaceShard{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "theOneAndOnlyShard",
						ClusterName: "root",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceShardSpec{
						Credentials: corev1.SecretReference{
							Name:      "kubeconfig",
							Namespace: "kcp",
						},
					},
				},
			},
			secrets: []corev1.Secret{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "kubeconfig",
						Namespace:   "kcp",
						ClusterName: "root",
					},
					Data: map[string][]byte{
						"kubeconfig": []byte(shardKubeConfigContent),
					},
				},
			},
			clusterRoleBindings: []rbacv1.ClusterRoleBinding{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        getRoleBindingName(OwnerRoleType, "foo", user),
						ClusterName: "root:orgName",
						Labels: map[string]string{
							PrettyNameLabel:   "foo",
							InternalNameLabel: "foo",
						},
					},
					Subjects: []rbacv1.Subject{
						{
							Kind: "User",
							Name: user.Name,
						},
					},
				},
			},
		},
		apply: func(t *testing.T, storage *REST, kubeconfigSubResourceStorage *KubeconfigSubresourceREST, ctx context.Context, kubeClient *fake.Clientset, kcpClient *tenancyv1fake.Clientset, listerCheckedUsers func() []kuser.Info, testData TestData) {
			response, err := kubeconfigSubResourceStorage.Get(ctx, "foo", nil)
			require.NoError(t, err)
			require.IsType(t, KubeConfig(""), response)
			responseWorkspace := response.(KubeConfig)
			assert.YAMLEq(t, expectedWorkspaceKubeconfigContent("personal"), string(responseWorkspace))
		},
	}
	applyTest(t, test)
}

func TestKubeconfigOrganizationWorkspace(t *testing.T) {
	user := &kuser.DefaultInfo{
		Name:   "test-user",
		UID:    "test-uid",
		Groups: []string{"test-group"},
	}
	test := TestDescription{
		TestData: TestData{
			user:    user,
			scope:   "organization",
			orgName: logicalcluster.New("root:orgName"),
			rootReviewer: workspaceauth.NewReviewer(&mockSubjectLocator{
				subjects: map[string]map[string][]rbacv1.Subject{
					"access/tenancy.kcp.dev/v1alpha1/clusterworkspaces/content": {
						"orgName": rbacGroups("test-group"),
					},
				},
			}),
			clusterWorkspaces: []tenancyv1alpha1.ClusterWorkspace{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "foo", ClusterName: "root:orgName"},
					Status: tenancyv1alpha1.ClusterWorkspaceStatus{
						BaseURL: "THE_RIGHT_SERVER_URL",
						Location: tenancyv1alpha1.ClusterWorkspaceLocation{
							Current: "theOneAndOnlyShard",
						},
						Conditions: conditionsv1alpha1.Conditions{
							{
								Type:   tenancyv1alpha1.WorkspaceShardValid,
								Status: corev1.ConditionTrue,
							},
						},
					},
				},
			},
			workspaceShards: []tenancyv1alpha1.ClusterWorkspaceShard{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "theOneAndOnlyShard",
						ClusterName: "root",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceShardSpec{
						Credentials: corev1.SecretReference{
							Name:      "kubeconfig",
							Namespace: "kcp",
						},
					},
				},
			},
			secrets: []corev1.Secret{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "kubeconfig",
						Namespace:   "kcp",
						ClusterName: "root",
					},
					Data: map[string][]byte{
						"kubeconfig": []byte(shardKubeConfigContent),
					},
				},
			},
			clusterRoleBindings: []rbacv1.ClusterRoleBinding{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        getRoleBindingName(OwnerRoleType, "foo", user),
						ClusterName: "root:orgName",
						Labels: map[string]string{
							PrettyNameLabel:   "foo",
							InternalNameLabel: "foo",
						},
					},
					Subjects: []rbacv1.Subject{
						{
							Kind: "User",
							Name: user.Name,
						},
					},
				},
			},
		},
		apply: func(t *testing.T, storage *REST, kubeconfigSubResourceStorage *KubeconfigSubresourceREST, ctx context.Context, kubeClient *fake.Clientset, kcpClient *tenancyv1fake.Clientset, listerCheckedUsers func() []kuser.Info, testData TestData) {
			response, err := kubeconfigSubResourceStorage.Get(ctx, "foo", nil)
			require.NoError(t, err)
			require.IsType(t, KubeConfig(""), response)
			responseWorkspace := response.(KubeConfig)
			assert.YAMLEq(t, expectedWorkspaceKubeconfigContent("organization"), string(responseWorkspace))
		},
	}
	applyTest(t, test)
}

func TestKubeconfigFailBecauseInvalidCADataBase64(t *testing.T) {
	user := &kuser.DefaultInfo{
		Name:   "test-user",
		UID:    "test-uid",
		Groups: []string{"test-group"},
	}
	test := TestDescription{
		TestData: TestData{
			user:    user,
			scope:   "organization",
			orgName: logicalcluster.New("root:orgName"),
			rootReviewer: workspaceauth.NewReviewer(&mockSubjectLocator{
				subjects: map[string]map[string][]rbacv1.Subject{
					"access/tenancy.kcp.dev/v1alpha1/clusterworkspaces/content": {
						"orgName": rbacGroups("test-group"),
					},
				},
			}),
			clusterWorkspaces: []tenancyv1alpha1.ClusterWorkspace{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "foo", ClusterName: "root:orgName"},
					Status: tenancyv1alpha1.ClusterWorkspaceStatus{
						BaseURL: "THE_RIGHT_SERVER_URL",
						Location: tenancyv1alpha1.ClusterWorkspaceLocation{
							Current: "theOneAndOnlyShard",
						},
						Conditions: conditionsv1alpha1.Conditions{
							{
								Type:   tenancyv1alpha1.WorkspaceShardValid,
								Status: corev1.ConditionTrue,
							},
						},
					},
				},
			},
			workspaceShards: []tenancyv1alpha1.ClusterWorkspaceShard{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "theOneAndOnlyShard",
						ClusterName: "root",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceShardSpec{
						Credentials: corev1.SecretReference{
							Name:      "kubeconfig",
							Namespace: "kcp",
						},
					},
				},
			},
			secrets: []corev1.Secret{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "kubeconfig",
						Namespace:   "kcp",
						ClusterName: "root",
					},
					Data: map[string][]byte{
						"kubeconfig": []byte(shardKubeConfigContentInvalidCADataBase64),
					},
				},
			},
			clusterRoleBindings: []rbacv1.ClusterRoleBinding{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        getRoleBindingName(OwnerRoleType, "foo", user),
						ClusterName: "root:orgName",
						Labels: map[string]string{
							PrettyNameLabel:   "foo",
							InternalNameLabel: "foo",
						},
					},
					Subjects: []rbacv1.Subject{
						{
							Kind: "User",
							Name: user.Name,
						},
					},
				},
			},
		},
		apply: func(t *testing.T, storage *REST, kubeconfigSubResourceStorage *KubeconfigSubresourceREST, ctx context.Context, kubeClient *fake.Clientset, kcpClient *tenancyv1fake.Clientset, listerCheckedUsers func() []kuser.Info, testData TestData) {
			_, err := kubeconfigSubResourceStorage.Get(ctx, "foo", nil)
			assert.EqualError(t, err, "workspaces/kubeconfig.tenancy.kcp.dev \"foo\" not found")
			var statusError *kerrors.StatusError
			require.ErrorAs(t, err, &statusError)
			require.Len(t, statusError.Status().Details.Causes, 1)
			assert.Equal(t, statusError.Status().Details.Causes[0].Type, metav1.CauseTypeUnexpectedServerResponse)
			assert.Regexp(t, "^ClusterWorkspace shard Kubeconfig is invalid: .*", statusError.Status().Details.Causes[0].Message)
			assert.Contains(t, statusError.Status().Details.Causes[0].Message, "illegal base64 data at input byte 7")
		},
	}
	applyTest(t, test)
}

func TestKubeconfigFailBecauseWithoutContext(t *testing.T) {
	user := &kuser.DefaultInfo{
		Name:   "test-user",
		UID:    "test-uid",
		Groups: []string{"test-group"},
	}
	test := TestDescription{
		TestData: TestData{
			user:    user,
			scope:   "organization",
			orgName: logicalcluster.New("root:orgName"),
			rootReviewer: workspaceauth.NewReviewer(&mockSubjectLocator{
				subjects: map[string]map[string][]rbacv1.Subject{
					"access/tenancy.kcp.dev/v1alpha1/clusterworkspaces/content": {
						"orgName": rbacGroups("test-group"),
					},
				},
			}),
			clusterWorkspaces: []tenancyv1alpha1.ClusterWorkspace{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "foo", ClusterName: "root:orgName"},
					Status: tenancyv1alpha1.ClusterWorkspaceStatus{
						BaseURL: "THE_RIGHT_SERVER_URL",
						Location: tenancyv1alpha1.ClusterWorkspaceLocation{
							Current: "theOneAndOnlyShard",
						},
						Conditions: conditionsv1alpha1.Conditions{
							{
								Type:   tenancyv1alpha1.WorkspaceShardValid,
								Status: corev1.ConditionTrue,
							},
						},
					},
				},
			},
			workspaceShards: []tenancyv1alpha1.ClusterWorkspaceShard{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "theOneAndOnlyShard",
						ClusterName: "root",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceShardSpec{
						Credentials: corev1.SecretReference{
							Name:      "kubeconfig",
							Namespace: "kcp",
						},
					},
				},
			},
			secrets: []corev1.Secret{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "kubeconfig",
						Namespace:   "kcp",
						ClusterName: "root",
					},
					Data: map[string][]byte{
						"kubeconfig": []byte(shardKubeConfigContentWithoutContext),
					},
				},
			},
			clusterRoleBindings: []rbacv1.ClusterRoleBinding{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        getRoleBindingName(OwnerRoleType, "foo", user),
						ClusterName: "root:orgName",
						Labels: map[string]string{
							PrettyNameLabel:   "foo",
							InternalNameLabel: "foo",
						},
					},
					Subjects: []rbacv1.Subject{
						{
							Kind: "User",
							Name: user.Name,
						},
					},
				},
			},
		},
		apply: func(t *testing.T, storage *REST, kubeconfigSubResourceStorage *KubeconfigSubresourceREST, ctx context.Context, kubeClient *fake.Clientset, kcpClient *tenancyv1fake.Clientset, listerCheckedUsers func() []kuser.Info, testData TestData) {
			_, err := kubeconfigSubResourceStorage.Get(ctx, "foo", nil)
			assert.EqualError(t, err, "workspaces/kubeconfig.tenancy.kcp.dev \"foo\" not found")
			var statusError *kerrors.StatusError
			require.ErrorAs(t, err, &statusError)
			require.Len(t, statusError.Status().Details.Causes, 1)
			assert.Equal(t, metav1.CauseTypeUnexpectedServerResponse, statusError.Status().Details.Causes[0].Type)
			assert.Equal(t, "Workspace shard Kubeconfig has no current context", statusError.Status().Details.Causes[0].Message)
		},
	}
	applyTest(t, test)
}

func TestKubeconfigFailBecauseInvalid(t *testing.T) {
	user := &kuser.DefaultInfo{
		Name:   "test-user",
		UID:    "test-uid",
		Groups: []string{"test-group"},
	}
	test := TestDescription{
		TestData: TestData{
			user:    user,
			scope:   "organization",
			orgName: logicalcluster.New("root:orgName"),
			rootReviewer: workspaceauth.NewReviewer(&mockSubjectLocator{
				subjects: map[string]map[string][]rbacv1.Subject{
					"access/tenancy.kcp.dev/v1alpha1/clusterworkspaces/content": {
						"orgName": rbacGroups("test-group"),
					},
				},
			}),
			clusterWorkspaces: []tenancyv1alpha1.ClusterWorkspace{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "foo", ClusterName: "root:orgName"},
					Status: tenancyv1alpha1.ClusterWorkspaceStatus{
						BaseURL: "THE_RIGHT_SERVER_URL",
						Location: tenancyv1alpha1.ClusterWorkspaceLocation{
							Current: "theOneAndOnlyShard",
						},
						Conditions: conditionsv1alpha1.Conditions{
							{
								Type:   tenancyv1alpha1.WorkspaceShardValid,
								Status: corev1.ConditionTrue,
							},
						},
					},
				},
			},
			workspaceShards: []tenancyv1alpha1.ClusterWorkspaceShard{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "theOneAndOnlyShard",
						ClusterName: "root",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceShardSpec{
						Credentials: corev1.SecretReference{
							Name:      "kubeconfig",
							Namespace: "kcp",
						},
					},
				},
			},
			secrets: []corev1.Secret{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "kubeconfig",
						Namespace:   "kcp",
						ClusterName: "root",
					},
					Data: map[string][]byte{
						"kubeconfig": []byte(shardKubeConfigContentInvalid),
					},
				},
			},
			clusterRoleBindings: []rbacv1.ClusterRoleBinding{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        getRoleBindingName(OwnerRoleType, "foo", user),
						ClusterName: "root:orgName",
						Labels: map[string]string{
							PrettyNameLabel:   "foo",
							InternalNameLabel: "foo",
						},
					},
					Subjects: []rbacv1.Subject{
						{
							Kind: "User",
							Name: user.Name,
						},
					},
				},
			},
		},
		apply: func(t *testing.T, storage *REST, kubeconfigSubResourceStorage *KubeconfigSubresourceREST, ctx context.Context, kubeClient *fake.Clientset, kcpClient *tenancyv1fake.Clientset, listerCheckedUsers func() []kuser.Info, testData TestData) {
			_, err := kubeconfigSubResourceStorage.Get(ctx, "foo", nil)
			assert.EqualError(t, err, "workspaces/kubeconfig.tenancy.kcp.dev \"foo\" not found")
			var statusError *kerrors.StatusError
			require.ErrorAs(t, err, &statusError)
			require.Len(t, statusError.Status().Details.Causes, 1)
			assert.Equal(t, metav1.CauseTypeUnexpectedServerResponse, statusError.Status().Details.Causes[0].Type)
			assert.Equal(t, "ClusterWorkspace shard Kubeconfig is invalid: yaml: line 5: could not find expected ':'", statusError.Status().Details.Causes[0].Message)
		},
	}
	applyTest(t, test)
}

func TestKubeconfigFailSecretDataNotFound(t *testing.T) {
	user := &kuser.DefaultInfo{
		Name:   "test-user",
		UID:    "test-uid",
		Groups: []string{"test-group"},
	}
	test := TestDescription{
		TestData: TestData{
			user:    user,
			scope:   "organization",
			orgName: logicalcluster.New("root:orgName"),
			rootReviewer: workspaceauth.NewReviewer(&mockSubjectLocator{
				subjects: map[string]map[string][]rbacv1.Subject{
					"access/tenancy.kcp.dev/v1alpha1/clusterworkspaces/content": {
						"orgName": rbacGroups("test-group"),
					},
				},
			}),
			clusterWorkspaces: []tenancyv1alpha1.ClusterWorkspace{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "foo", ClusterName: "root:orgName"},
					Status: tenancyv1alpha1.ClusterWorkspaceStatus{
						BaseURL: "THE_RIGHT_SERVER_URL",
						Location: tenancyv1alpha1.ClusterWorkspaceLocation{
							Current: "theOneAndOnlyShard",
						},
						Conditions: conditionsv1alpha1.Conditions{
							{
								Type:   tenancyv1alpha1.WorkspaceShardValid,
								Status: corev1.ConditionTrue,
							},
						},
					},
				},
			},
			workspaceShards: []tenancyv1alpha1.ClusterWorkspaceShard{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "theOneAndOnlyShard",
						ClusterName: "root",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceShardSpec{
						Credentials: corev1.SecretReference{
							Name:      "kubeconfig",
							Namespace: "kcp",
						},
					},
				},
			},
			secrets: []corev1.Secret{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "kubeconfig",
						Namespace:   "kcp",
						ClusterName: "root",
					},
				},
			},
			clusterRoleBindings: []rbacv1.ClusterRoleBinding{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        getRoleBindingName(OwnerRoleType, "foo", user),
						ClusterName: "root:orgName",
						Labels: map[string]string{
							PrettyNameLabel:   "foo",
							InternalNameLabel: "foo",
						},
					},
					Subjects: []rbacv1.Subject{
						{
							Kind: "User",
							Name: user.Name,
						},
					},
				},
			},
		},
		apply: func(t *testing.T, storage *REST, kubeconfigSubResourceStorage *KubeconfigSubresourceREST, ctx context.Context, kubeClient *fake.Clientset, kcpClient *tenancyv1fake.Clientset, listerCheckedUsers func() []kuser.Info, testData TestData) {
			_, err := kubeconfigSubResourceStorage.Get(ctx, "foo", nil)
			assert.EqualError(t, err, "workspaces/kubeconfig.tenancy.kcp.dev \"foo\" not found")
			var statusError *kerrors.StatusError
			require.ErrorAs(t, err, &statusError)
			require.Len(t, statusError.Status().Details.Causes, 1)
			assert.Equal(t, metav1.CauseTypeUnexpectedServerResponse, statusError.Status().Details.Causes[0].Type)
			assert.Equal(t, "Key 'kubeconfig' not found in workspace shard Kubeconfig secret", statusError.Status().Details.Causes[0].Message)
		},
	}
	applyTest(t, test)
}

func TestKubeconfigFailBecauseSecretNotFound(t *testing.T) {
	user := &kuser.DefaultInfo{
		Name:   "test-user",
		UID:    "test-uid",
		Groups: []string{"test-group"},
	}
	test := TestDescription{
		TestData: TestData{
			user:    user,
			scope:   "organization",
			orgName: logicalcluster.New("root:orgName"),
			rootReviewer: workspaceauth.NewReviewer(&mockSubjectLocator{
				subjects: map[string]map[string][]rbacv1.Subject{
					"access/tenancy.kcp.dev/v1alpha1/clusterworkspaces/content": {
						"orgName": rbacGroups("test-group"),
					},
				},
			}),
			clusterWorkspaces: []tenancyv1alpha1.ClusterWorkspace{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "foo", ClusterName: "root:orgName"},
					Status: tenancyv1alpha1.ClusterWorkspaceStatus{
						BaseURL: "THE_RIGHT_SERVER_URL",
						Location: tenancyv1alpha1.ClusterWorkspaceLocation{
							Current: "theOneAndOnlyShard",
						},
						Conditions: conditionsv1alpha1.Conditions{
							{
								Type:   tenancyv1alpha1.WorkspaceShardValid,
								Status: corev1.ConditionTrue,
							},
						},
					},
				},
			},
			workspaceShards: []tenancyv1alpha1.ClusterWorkspaceShard{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "theOneAndOnlyShard",
						ClusterName: "root",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceShardSpec{
						Credentials: corev1.SecretReference{
							Name:      "kubeconfig",
							Namespace: "kcp",
						},
					},
				},
			},
			clusterRoleBindings: []rbacv1.ClusterRoleBinding{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        getRoleBindingName(OwnerRoleType, "foo", user),
						ClusterName: "root:orgName",
						Labels: map[string]string{
							PrettyNameLabel:   "foo",
							InternalNameLabel: "foo",
						},
					},
					Subjects: []rbacv1.Subject{
						{
							Kind: "User",
							Name: user.Name,
						},
					},
				},
			},
		},
		apply: func(t *testing.T, storage *REST, kubeconfigSubResourceStorage *KubeconfigSubresourceREST, ctx context.Context, kubeClient *fake.Clientset, kcpClient *tenancyv1fake.Clientset, listerCheckedUsers func() []kuser.Info, testData TestData) {
			_, err := kubeconfigSubResourceStorage.Get(ctx, "foo", nil)
			assert.EqualError(t, err, "workspaces/kubeconfig.tenancy.kcp.dev \"foo\" not found")
			var statusError *kerrors.StatusError
			require.ErrorAs(t, err, &statusError)
			require.Len(t, statusError.Status().Details.Causes, 1)
			assert.Equal(t, metav1.CauseTypeUnexpectedServerResponse, statusError.Status().Details.Causes[0].Type)
			assert.Equal(t, "secrets \"kubeconfig\" not found", statusError.Status().Details.Causes[0].Message)
		},
	}
	applyTest(t, test)
}

func TestKubeconfigFailBecauseShardNotFound(t *testing.T) {
	user := &kuser.DefaultInfo{
		Name:   "test-user",
		UID:    "test-uid",
		Groups: []string{"test-group"},
	}
	test := TestDescription{
		TestData: TestData{
			user:    user,
			scope:   "organization",
			orgName: logicalcluster.New("root:orgName"),
			rootReviewer: workspaceauth.NewReviewer(&mockSubjectLocator{
				subjects: map[string]map[string][]rbacv1.Subject{
					"access/tenancy.kcp.dev/v1alpha1/clusterworkspaces/content": {
						"orgName": rbacGroups("test-group"),
					},
				},
			}),
			clusterWorkspaces: []tenancyv1alpha1.ClusterWorkspace{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "foo", ClusterName: "root:orgName"},
					Status: tenancyv1alpha1.ClusterWorkspaceStatus{
						BaseURL: "THE_RIGHT_SERVER_URL",
						Location: tenancyv1alpha1.ClusterWorkspaceLocation{
							Current: "theOneAndOnlyShard",
						},
						Conditions: conditionsv1alpha1.Conditions{
							{
								Type:   tenancyv1alpha1.WorkspaceShardValid,
								Status: corev1.ConditionTrue,
							},
						},
					},
				},
			},
			clusterRoleBindings: []rbacv1.ClusterRoleBinding{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        getRoleBindingName(OwnerRoleType, "foo", user),
						ClusterName: "root:orgName",
						Labels: map[string]string{
							PrettyNameLabel:   "foo",
							InternalNameLabel: "foo",
						},
					},
					Subjects: []rbacv1.Subject{
						{
							Kind: "User",
							Name: user.Name,
						},
					},
				},
			},
		},
		apply: func(t *testing.T, storage *REST, kubeconfigSubResourceStorage *KubeconfigSubresourceREST, ctx context.Context, kubeClient *fake.Clientset, kcpClient *tenancyv1fake.Clientset, listerCheckedUsers func() []kuser.Info, testData TestData) {
			_, err := kubeconfigSubResourceStorage.Get(ctx, "foo", nil)
			assert.EqualError(t, err, "workspaces/kubeconfig.tenancy.kcp.dev \"foo\" not found")
			var statusError *kerrors.StatusError
			require.ErrorAs(t, err, &statusError)
			require.Len(t, statusError.Status().Details.Causes, 1)
			assert.Equal(t, metav1.CauseTypeUnexpectedServerResponse, statusError.Status().Details.Causes[0].Type)
			assert.Equal(t, "workspaceshards.tenancy.kcp.dev \"theOneAndOnlyShard\" not found", statusError.Status().Details.Causes[0].Message)
		},
	}
	applyTest(t, test)
}

func TestKubeconfigFailBecauseWorkspaceNotFound(t *testing.T) {
	user := &kuser.DefaultInfo{
		Name:   "test-user",
		UID:    "test-uid",
		Groups: []string{"test-group"},
	}
	test := TestDescription{
		TestData: TestData{
			user:    user,
			scope:   "organization",
			orgName: logicalcluster.New("root:orgName"),
			rootReviewer: workspaceauth.NewReviewer(&mockSubjectLocator{
				subjects: map[string]map[string][]rbacv1.Subject{
					"access/tenancy.kcp.dev/v1alpha1/clusterworkspaces/content": {
						"orgName": rbacGroups("test-group"),
					},
				},
			}),
		},
		apply: func(t *testing.T, storage *REST, kubeconfigSubResourceStorage *KubeconfigSubresourceREST, ctx context.Context, kubeClient *fake.Clientset, kcpClient *tenancyv1fake.Clientset, listerCheckedUsers func() []kuser.Info, testData TestData) {
			_, err := kubeconfigSubResourceStorage.Get(ctx, "foo", nil)
			assert.EqualError(t, err, "workspaces.tenancy.kcp.dev \"foo\" not found")
			var statusError *kerrors.StatusError
			require.ErrorAs(t, err, &statusError)
			require.Len(t, statusError.Status().Details.Causes, 0)
		},
	}
	applyTest(t, test)
}
