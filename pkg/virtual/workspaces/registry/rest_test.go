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
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	kuser "k8s.io/apiserver/pkg/authentication/user"
	apirequest "k8s.io/apiserver/pkg/endpoints/request"
	informers "k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
	"k8s.io/kubernetes/pkg/controller"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
	tenancyv1fake "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/fake"
	workspaceauth "github.com/kcp-dev/kcp/pkg/virtual/workspaces/auth"
)

// mockLister returns the workspaces in the list
type mockLister struct {
	checkedUsers []kuser.Info
	workspaces   []tenancyv1alpha1.ClusterWorkspace
}

func (m *mockLister) CheckedUsers() []kuser.Info {
	return m.checkedUsers
}

func (ml *mockLister) List(user kuser.Info, selector labels.Selector) (*tenancyv1alpha1.ClusterWorkspaceList, error) {
	ml.checkedUsers = append(ml.checkedUsers, user)
	return &tenancyv1alpha1.ClusterWorkspaceList{
		Items: ml.workspaces,
	}, nil
}

var _ workspaceauth.Review = mockReview{}

type mockReview struct {
	users           []string
	groups          []string
	evaluationError string
}

func (m mockReview) Users() []string {
	return m.users
}
func (m mockReview) Groups() []string {
	return m.groups
}
func (m mockReview) EvaluationError() string {
	return m.evaluationError
}

var _ workspaceauth.Reviewer = mockReviewer{}

type mockReviewer map[string]mockReview

func (m mockReviewer) Review(name string) (workspaceauth.Review, error) {
	return m[name], nil
}

var _ workspaceauth.ReviewerProvider = mockReviewerProvider{}

type mockReviewerProvider map[string]mockReviewer

func (m mockReviewerProvider) ForVerb(checkedVerb string) workspaceauth.Reviewer {
	return m[checkedVerb]
}

type TestData struct {
	clusterRoles        []rbacv1.ClusterRole
	clusterRoleBindings []rbacv1.ClusterRoleBinding
	clusterWorkspaces   []tenancyv1alpha1.ClusterWorkspace
	workspaceShards     []tenancyv1alpha1.WorkspaceShard
	secrets             []corev1.Secret
	workspaceLister     *mockLister
	user                kuser.Info
	scope               string
	reviewerProvider    workspaceauth.ReviewerProvider
}

type TestDescription struct {
	TestData
	apply func(t *testing.T, storage *REST, kubeconfigSubResourceStorage *KubeconfigSubresourceREST, ctx context.Context, kubeClient *fake.Clientset, kcpClient *tenancyv1fake.Clientset, listerCheckedUsers func() []kuser.Info, testData TestData)
}

func applyTest(t *testing.T, test TestDescription) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	watcherStarted := make(chan struct{})

	workspaceList := tenancyv1alpha1.ClusterWorkspaceList{
		Items: test.clusterWorkspaces,
	}
	workspaceShardList := tenancyv1alpha1.WorkspaceShardList{
		Items: test.workspaceShards,
	}
	crbList := rbacv1.ClusterRoleBindingList{
		Items: test.clusterRoleBindings,
	}
	crList := rbacv1.ClusterRoleList{
		Items: test.clusterRoles,
	}
	secretList := corev1.SecretList{
		Items: test.secrets,
	}
	mockKCPClient := tenancyv1fake.NewSimpleClientset(&workspaceList, &workspaceShardList)
	mockKubeClient := fake.NewSimpleClientset(&crbList, &crList, &secretList)
	mockKubeClient.PrependWatchReactor("*", func(action clienttesting.Action) (handled bool, ret watch.Interface, err error) {
		gvr := action.GetResource()
		ns := action.GetNamespace()
		watch, err := mockKubeClient.Tracker().Watch(gvr, ns)
		if err != nil {
			return false, nil, err
		}
		close(watcherStarted)
		return true, watch, nil
	})
	mockKubeClient.AddReactor("delete-collection", "*", func(action clienttesting.Action) (handled bool, ret runtime.Object, err error) {
		deleteCollectionAction := action.(clienttesting.DeleteCollectionAction)
		var gvr schema.GroupVersionResource = deleteCollectionAction.GetResource()
		var gvk schema.GroupVersionKind
		switch gvr.Resource {
		case "clusterroles":
			gvk = gvr.GroupVersion().WithKind("ClusterRole")
		case "clusterrolebindings":
			gvk = gvr.GroupVersion().WithKind("ClusterRoleBinding")
		default:
			return false, nil, nil
		}

		list, err := mockKubeClient.Tracker().List(gvr, gvk, "")
		if err != nil {
			return false, nil, err
		}
		items := reflect.ValueOf(list).Elem().FieldByName("Items")
		for i := 0; i < items.Len(); i++ {
			item := items.Index(i).Addr().Interface()
			object := item.(metav1.Object)
			objectLabels := object.GetLabels()
			if deleteCollectionAction.GetListRestrictions().Labels.Matches(labels.Set(objectLabels)) {
				if err := mockKubeClient.Tracker().Delete(gvr, "", object.GetName()); err != nil {
					return false, nil, err
				}
			}
		}
		return true, nil, nil
	})

	kubeInformers := informers.NewSharedInformerFactory(mockKubeClient, controller.NoResyncPeriodFunc())
	crbInformer := kubeInformers.Rbac().V1().ClusterRoleBindings()
	_ = AddNameIndexers(crbInformer)

	// Make sure informers are running.
	kubeInformers.Start(ctx.Done())

	// This is not required in tests, but it serves as a proof-of-concept by
	// ensuring that the informer goroutine have warmed up and called List before
	// we send any events to it.
	cache.WaitForCacheSync(ctx.Done(), crbInformer.Informer().HasSynced)

	// The fake client doesn't support resource version. Any writes to the client
	// after the informer's initial LIST and before the informer establishing the
	// watcher will be missed by the informer. Therefore we wait until the watcher
	// starts.
	// Note that the fake client isn't designed to work with informer. It
	// doesn't support resource version. It's encouraged to use a real client
	// in an integration/E2E test if you need to test complex behavior with
	// informer/controllers.
	<-watcherStarted

	clusterWorkspaceLister := test.workspaceLister
	if clusterWorkspaceLister == nil {
		clusterWorkspaceLister = &mockLister{
			workspaces: test.clusterWorkspaces,
		}
	}

	storage := REST{
		rbacClient:                mockKubeClient.RbacV1(),
		crbInformer:               crbInformer,
		clusterWorkspaceClient:    mockKCPClient.TenancyV1alpha1().ClusterWorkspaces(),
		crbLister:                 kubeInformers.Rbac().V1().ClusterRoleBindings().Lister(),
		clusterWorkspaceLister:    clusterWorkspaceLister,
		workspaceReviewerProvider: test.reviewerProvider,
	}
	kubeconfigSubresourceStorage := KubeconfigSubresourceREST{
		mainRest:             &storage,
		rootCoreClient:       mockKubeClient.CoreV1(),
		workspaceShardClient: mockKCPClient.TenancyV1alpha1().WorkspaceShards(),
	}
	ctx = apirequest.WithUser(ctx, test.user)
	ctx = apirequest.WithValue(ctx, WorkspacesScopeKey, test.scope)

	test.apply(t, &storage, &kubeconfigSubresourceStorage, ctx, mockKubeClient, mockKCPClient, clusterWorkspaceLister.CheckedUsers, test.TestData)
}

func TestListPersonalWorkspaces(t *testing.T) {
	user := &kuser.DefaultInfo{
		Name:   "test-user",
		UID:    "test-uid",
		Groups: []string{"test-group"},
	}
	test := TestDescription{
		TestData: TestData{
			user:  user,
			scope: PersonalScope,
			reviewerProvider: mockReviewerProvider{
				"get":    mockReviewer{},
				"delete": mockReviewer{},
			},
			clusterWorkspaces: []tenancyv1alpha1.ClusterWorkspace{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "foo"},
				},
			},
			clusterRoleBindings: []rbacv1.ClusterRoleBinding{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: getRoleBindingName(OwnerRoleType, "foo", user),
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
			response, err := storage.List(ctx, nil)
			require.NoError(t, err)
			workspaces := response.(*tenancyv1beta1.WorkspaceList)
			require.Len(t, workspaces.Items, 1, "workspaces.Items should have len 1")
			responseClusterWorkspace := workspaces.Items[0]
			assert.Equal(t, "foo", responseClusterWorkspace.Name)
			checkedUsers := listerCheckedUsers()
			require.Len(t, checkedUsers, 1, "The workspaceLister should have checked only 1 user")
			assert.Equal(t,
				&kuser.DefaultInfo{
					Name:   user.Name,
					UID:    user.UID,
					Groups: []string{},
				},
				checkedUsers[0],
				"The workspaceLister should have checked the user without its groups")
		},
	}
	applyTest(t, test)
}

func TestListPersonalWorkspacesWithPrettyName(t *testing.T) {
	user := &kuser.DefaultInfo{
		Name:   "test-user",
		UID:    "test-uid",
		Groups: []string{"test-group"},
	}
	test := TestDescription{
		TestData: TestData{
			user:  user,
			scope: PersonalScope,
			reviewerProvider: mockReviewerProvider{
				"get":    mockReviewer{},
				"delete": mockReviewer{},
			},
			clusterWorkspaces: []tenancyv1alpha1.ClusterWorkspace{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "foo--1"},
				},
			},
			clusterRoleBindings: []rbacv1.ClusterRoleBinding{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: getRoleBindingName(OwnerRoleType, "foo", user),
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
			response, err := storage.List(ctx, nil)
			require.NoError(t, err)
			workspaces := response.(*tenancyv1beta1.WorkspaceList)
			require.Len(t, workspaces.Items, 1, "workspaces.Items should have len 1")
			responseWorkspace := workspaces.Items[0]
			assert.Equal(t, "foo", responseWorkspace.Name)
			checkedUsers := listerCheckedUsers()
			require.Len(t, checkedUsers, 1, "The workspaceLister should have checked only 1 user")
			assert.Equal(t,
				&kuser.DefaultInfo{
					Name:   user.Name,
					UID:    user.UID,
					Groups: []string{},
				},
				checkedUsers[0],
				"The workspaceLister should have checked the user without its groups")

			if err != nil {
				t.Errorf("%#v should be nil.", err)
			}
		},
	}
	applyTest(t, test)
}

func TestListOrganizationWorkspaces(t *testing.T) {
	user := &kuser.DefaultInfo{
		Name:   "test-user",
		UID:    "test-uid",
		Groups: []string{"test-group"},
	}
	test := TestDescription{
		TestData: TestData{
			user:  user,
			scope: OrganizationScope,
			reviewerProvider: mockReviewerProvider{
				"get":    mockReviewer{},
				"delete": mockReviewer{},
			},
			clusterWorkspaces: []tenancyv1alpha1.ClusterWorkspace{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "foo"},
				},
			},
			clusterRoleBindings: []rbacv1.ClusterRoleBinding{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: getRoleBindingName(OwnerRoleType, "foo", user),
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
			response, err := storage.List(ctx, nil)
			require.NoError(t, err)
			workspaces := response.(*tenancyv1beta1.WorkspaceList)
			require.Len(t, workspaces.Items, 1, "workspaces.Items should have len 1")
			responseWorkspace := workspaces.Items[0]
			assert.Equal(t, "foo", responseWorkspace.Name)
			checkedUsers := listerCheckedUsers()
			require.Len(t, checkedUsers, 1, "The workspaceLister should have checked only 1 user")
			assert.Equal(t,
				user,
				checkedUsers[0],
				"The workspaceLister should have checked the user with its groups")
		},
	}
	applyTest(t, test)
}

func TestListOrganizationWorkspacesWithPrettyName(t *testing.T) {
	user := &kuser.DefaultInfo{
		Name:   "test-user",
		UID:    "test-uid",
		Groups: []string{"test-group"},
	}
	test := TestDescription{
		TestData: TestData{
			user:  user,
			scope: OrganizationScope,
			reviewerProvider: mockReviewerProvider{
				"get":    mockReviewer{},
				"delete": mockReviewer{},
			},
			clusterWorkspaces: []tenancyv1alpha1.ClusterWorkspace{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "foo--1"},
				},
			},
			clusterRoleBindings: []rbacv1.ClusterRoleBinding{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: getRoleBindingName(OwnerRoleType, "foo", user),
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
			response, err := storage.List(ctx, nil)
			require.NoError(t, err)
			workspaces := response.(*tenancyv1beta1.WorkspaceList)
			require.Len(t, workspaces.Items, 1, "workspaces.Items should have len 1")
			responseWorkspace := workspaces.Items[0]
			assert.Equal(t, "foo--1", responseWorkspace.Name)
			checkedUsers := listerCheckedUsers()
			require.Len(t, checkedUsers, 1, "The workspaceLister should have checked only 1 user")
			assert.Equal(t,
				user,
				checkedUsers[0],
				"The workspaceLister should have checked the user with its groups")
		},
	}
	applyTest(t, test)
}

func TestGetPersonalWorkspace(t *testing.T) {
	user := &kuser.DefaultInfo{
		Name:   "test-user",
		UID:    "test-uid",
		Groups: []string{"test-group"},
	}
	test := TestDescription{
		TestData: TestData{
			user:  user,
			scope: PersonalScope,
			reviewerProvider: mockReviewerProvider{
				"get":    mockReviewer{},
				"delete": mockReviewer{},
			},
			clusterWorkspaces: []tenancyv1alpha1.ClusterWorkspace{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "foo"},
				},
			},
			clusterRoleBindings: []rbacv1.ClusterRoleBinding{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: getRoleBindingName(OwnerRoleType, "foo", user),
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
			response, err := storage.Get(ctx, "foo", nil)
			require.NoError(t, err)
			require.IsType(t, &tenancyv1beta1.Workspace{}, response)
			responseWorkspace := response.(*tenancyv1beta1.Workspace)
			assert.Equal(t, "foo", responseWorkspace.Name)
			checkedUsers := listerCheckedUsers()
			require.Len(t, checkedUsers, 1, "The workspaceLister should have checked only 1 user")
			assert.Equal(t,
				&kuser.DefaultInfo{
					Name:   user.Name,
					UID:    user.UID,
					Groups: []string{},
				},
				checkedUsers[0],
				"The workspaceLister should have checked the user without its groups")
		},
	}
	applyTest(t, test)
}

func TestGetPersonalWorkspaceWithPrettyName(t *testing.T) {
	user := &kuser.DefaultInfo{
		Name:   "test-user",
		UID:    "test-uid",
		Groups: []string{"test-group"},
	}
	test := TestDescription{
		TestData: TestData{
			user:  user,
			scope: PersonalScope,
			reviewerProvider: mockReviewerProvider{
				"get":    mockReviewer{},
				"delete": mockReviewer{},
			},
			clusterWorkspaces: []tenancyv1alpha1.ClusterWorkspace{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "foo--1"},
				},
			},
			clusterRoleBindings: []rbacv1.ClusterRoleBinding{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: getRoleBindingName(OwnerRoleType, "foo", user),
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
			response, err := storage.Get(ctx, "foo", nil)
			require.NoError(t, err)
			require.IsType(t, &tenancyv1beta1.Workspace{}, response)
			responseWorkspace := response.(*tenancyv1beta1.Workspace)
			assert.Equal(t, "foo", responseWorkspace.Name)
			checkedUsers := listerCheckedUsers()
			require.Len(t, checkedUsers, 1, "The workspaceLister should have checked only 1 user")
			assert.Equal(t,
				&kuser.DefaultInfo{
					Name:   user.Name,
					UID:    user.UID,
					Groups: []string{},
				},
				checkedUsers[0],
				"The workspaceLister should have checked the user without its groups")
		},
	}
	applyTest(t, test)
}

func TestGetPersonalWorkspaceNotFoundNoPermission(t *testing.T) {
	user := &kuser.DefaultInfo{
		Name:   "test-user",
		UID:    "test-uid",
		Groups: []string{"test-group"},
	}
	test := TestDescription{
		TestData: TestData{
			user:  user,
			scope: PersonalScope,
			reviewerProvider: mockReviewerProvider{
				"get":    mockReviewer{},
				"delete": mockReviewer{},
			},
			clusterWorkspaces: []tenancyv1alpha1.ClusterWorkspace{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "foo"},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "foo2"},
				},
			},
			workspaceLister: &mockLister{
				workspaces: []tenancyv1alpha1.ClusterWorkspace{
					{
						ObjectMeta: metav1.ObjectMeta{Name: "foo2"},
					},
				},
			},
			clusterRoleBindings: []rbacv1.ClusterRoleBinding{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: getRoleBindingName(OwnerRoleType, "foo", user),
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
			response, err := storage.Get(ctx, "foo", nil)
			require.Error(t, err)
			require.Nil(t, response)
			checkedUsers := listerCheckedUsers()
			require.Len(t, checkedUsers, 1, "The workspaceLister should have checked only 1 user")
			assert.Equal(t,
				&kuser.DefaultInfo{
					Name:   user.Name,
					UID:    user.UID,
					Groups: []string{},
				},
				checkedUsers[0],
				"The workspaceLister should have checked the user without its groups")
		},
	}
	applyTest(t, test)
}

func TestCreateWorkspaceInOrganizationNotAllowed(t *testing.T) {
	user := &kuser.DefaultInfo{
		Name:   "test-user",
		UID:    "test-uid",
		Groups: []string{"test-group"},
	}
	test := TestDescription{
		TestData: TestData{
			user:  user,
			scope: OrganizationScope,
			reviewerProvider: mockReviewerProvider{
				"get":    mockReviewer{},
				"delete": mockReviewer{},
			},
		},
		apply: func(t *testing.T, storage *REST, kubeconfigSubResourceStorage *KubeconfigSubresourceREST, ctx context.Context, kubeClient *fake.Clientset, kcpClient *tenancyv1fake.Clientset, listerCheckedUsers func() []kuser.Info, testData TestData) {
			newWorkspace := tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "foo",
				},
			}
			response, err := storage.Create(ctx, &newWorkspace, nil, &metav1.CreateOptions{})
			require.EqualError(t, err, "workspace.tenancy.kcp.dev is forbidden: creating a workspace in only possible in the personal workspaces scope for now")
			require.Nil(t, response)
			checkedUsers := listerCheckedUsers()
			require.Len(t, checkedUsers, 0, "The workspaceLister shouldn't have checked any user")
		},
	}
	applyTest(t, test)
}

func TestCreateWorkspace(t *testing.T) {
	user := &kuser.DefaultInfo{
		Name:   "test-user",
		UID:    "test-uid",
		Groups: []string{"test-group"},
	}
	test := TestDescription{
		TestData: TestData{
			user:  user,
			scope: PersonalScope,
			reviewerProvider: mockReviewerProvider{
				"get":    mockReviewer{},
				"delete": mockReviewer{},
			},
		},
		apply: func(t *testing.T, storage *REST, kubeconfigSubResourceStorage *KubeconfigSubresourceREST, ctx context.Context, kubeClient *fake.Clientset, kcpClient *tenancyv1fake.Clientset, listerCheckedUsers func() []kuser.Info, testData TestData) {
			newWorkspace := tenancyv1beta1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "foo",
				},
			}
			response, err := storage.Create(ctx, &newWorkspace, nil, &metav1.CreateOptions{})
			require.NoError(t, err)
			require.NotNil(t, response)
			require.IsType(t, &tenancyv1beta1.Workspace{}, response)
			workspace := response.(*tenancyv1beta1.Workspace)
			assert.Equal(t, "foo", workspace.Name)
			crbList, err := kubeClient.Tracker().List(rbacv1.SchemeGroupVersion.WithResource("clusterrolebindings"), rbacv1.SchemeGroupVersion.WithKind("ClusterRoleBinding"), "")
			require.NoError(t, err)
			crbs := crbList.(*rbacv1.ClusterRoleBindingList)
			assert.ElementsMatch(t, crbs.Items, append(testData.clusterRoleBindings,
				rbacv1.ClusterRoleBinding{
					ObjectMeta: metav1.ObjectMeta{
						Name: "owner-workspace-foo-test-user",
						Labels: map[string]string{
							PrettyNameLabel:   "foo",
							InternalNameLabel: "foo",
						},
					},
					RoleRef: rbacv1.RoleRef{
						APIGroup: "rbac.authorization.k8s.io",
						Kind:     "ClusterRole",
						Name:     "owner-workspace-foo-test-user",
					},
					Subjects: []rbacv1.Subject{
						{
							Kind: "User",
							Name: "test-user",
						},
					},
				},
			))
			crList, err := kubeClient.Tracker().List(rbacv1.SchemeGroupVersion.WithResource("clusterroles"), rbacv1.SchemeGroupVersion.WithKind("ClusterRole"), "")
			require.NoError(t, err)
			crs := crList.(*rbacv1.ClusterRoleList)
			assert.ElementsMatch(t, crs.Items, append(testData.clusterRoles,
				rbacv1.ClusterRole{
					ObjectMeta: metav1.ObjectMeta{
						Name: "lister-workspace-foo-test-user",
						Labels: map[string]string{
							InternalNameLabel: "foo",
						},
					},
					Rules: []rbacv1.PolicyRule{
						{
							Verbs:         []string{"get"},
							ResourceNames: []string{"foo"},
							Resources:     []string{"workspaces"},
							APIGroups:     []string{"tenancy.kcp.dev"},
						},
						{
							Verbs:         []string{"view"},
							ResourceNames: []string{"foo"},
							Resources:     []string{"workspaces/content"},
							APIGroups:     []string{"tenancy.kcp.dev"},
						},
					},
				},
				rbacv1.ClusterRole{
					ObjectMeta: metav1.ObjectMeta{
						Name: "owner-workspace-foo-test-user",
						Labels: map[string]string{
							InternalNameLabel: "foo",
						},
					},
					Rules: []rbacv1.PolicyRule{
						{
							Verbs:         []string{"get", "delete"},
							ResourceNames: []string{"foo"},
							Resources:     []string{"workspaces"},
							APIGroups:     []string{"tenancy.kcp.dev"},
						},
						{
							Verbs:         []string{"view", "edit"},
							ResourceNames: []string{"foo"},
							Resources:     []string{"workspaces/content"},
							APIGroups:     []string{"tenancy.kcp.dev"},
						},
					},
				},
			))
		},
	}
	applyTest(t, test)
}

func TestCreateWorkspaceWithPrettyName(t *testing.T) {
	anotherUser := &kuser.DefaultInfo{
		Name:   "another-user",
		UID:    "another-uid",
		Groups: []string{},
	}
	user := &kuser.DefaultInfo{
		Name:   "test-user",
		UID:    "test-uid",
		Groups: []string{"test-group"},
	}
	test := TestDescription{
		TestData: TestData{
			user:  user,
			scope: PersonalScope,
			reviewerProvider: mockReviewerProvider{
				"get":    mockReviewer{},
				"delete": mockReviewer{},
			},
			clusterWorkspaces: []tenancyv1alpha1.ClusterWorkspace{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "foo"},
				},
			},
			clusterRoleBindings: []rbacv1.ClusterRoleBinding{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: getRoleBindingName(OwnerRoleType, "foo", anotherUser),
						Labels: map[string]string{
							PrettyNameLabel:   "foo",
							InternalNameLabel: "foo",
						},
					},
					Subjects: []rbacv1.Subject{
						{
							Kind: "User",
							Name: anotherUser.Name,
						},
					},
				},
			},
			clusterRoles: []rbacv1.ClusterRole{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: getRoleBindingName(OwnerRoleType, "foo", anotherUser),
						Labels: map[string]string{
							InternalNameLabel: "foo",
						},
					},
					Rules: []rbacv1.PolicyRule{
						{
							Verbs:         []string{"get", "delete"},
							ResourceNames: []string{"foo"},
							Resources:     []string{"workspaces"},
							APIGroups:     []string{"tenancy.kcp.dev"},
						},
						{
							Verbs:         []string{"view", "edit"},
							ResourceNames: []string{"foo"},
							Resources:     []string{"workspaces/content"},
							APIGroups:     []string{"tenancy.kcp.dev"},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: getRoleBindingName(ListerRoleType, "foo", anotherUser),
						Labels: map[string]string{
							InternalNameLabel: "foo",
						},
					},
					Rules: []rbacv1.PolicyRule{
						{
							Verbs:         []string{"get"},
							ResourceNames: []string{"foo"},
							Resources:     []string{"workspaces"},
							APIGroups:     []string{"tenancy.kcp.dev"},
						},
						{
							Verbs:         []string{"view"},
							ResourceNames: []string{"foo"},
							Resources:     []string{"workspaces/content"},
							APIGroups:     []string{"tenancy.kcp.dev"},
						},
					},
				},
			},
		},
		apply: func(t *testing.T, storage *REST, kubeconfigSubResourceStorage *KubeconfigSubresourceREST, ctx context.Context, kubeClient *fake.Clientset, kcpClient *tenancyv1fake.Clientset, listerCheckedUsers func() []kuser.Info, testData TestData) {
			newWorkspace := tenancyv1beta1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "foo",
				},
			}
			response, err := storage.Create(ctx, &newWorkspace, nil, &metav1.CreateOptions{})
			require.NoError(t, err)
			require.NotNil(t, response)
			require.IsType(t, &tenancyv1beta1.Workspace{}, response)
			workspace := response.(*tenancyv1beta1.Workspace)
			assert.Equal(t, "foo", workspace.Name)
			crbList, err := kubeClient.Tracker().List(rbacv1.SchemeGroupVersion.WithResource("clusterrolebindings"), rbacv1.SchemeGroupVersion.WithKind("ClusterRoleBinding"), "")
			require.NoError(t, err)
			crbs := crbList.(*rbacv1.ClusterRoleBindingList)
			assert.ElementsMatch(t, crbs.Items, append(testData.clusterRoleBindings,
				rbacv1.ClusterRoleBinding{
					ObjectMeta: metav1.ObjectMeta{
						Name: "owner-workspace-foo-test-user",
						Labels: map[string]string{
							PrettyNameLabel:   "foo",
							InternalNameLabel: "foo--1",
						},
					},
					RoleRef: rbacv1.RoleRef{
						APIGroup: "rbac.authorization.k8s.io",
						Kind:     "ClusterRole",
						Name:     "owner-workspace-foo-test-user",
					},
					Subjects: []rbacv1.Subject{
						{
							Kind: "User",
							Name: "test-user",
						},
					},
				},
			))
			crList, err := kubeClient.Tracker().List(rbacv1.SchemeGroupVersion.WithResource("clusterroles"), rbacv1.SchemeGroupVersion.WithKind("ClusterRole"), "")
			require.NoError(t, err)
			crs := crList.(*rbacv1.ClusterRoleList)
			assert.ElementsMatch(t, crs.Items, append(testData.clusterRoles,
				rbacv1.ClusterRole{
					ObjectMeta: metav1.ObjectMeta{
						Name: "lister-workspace-foo-test-user",
						Labels: map[string]string{
							InternalNameLabel: "foo--1",
						},
					},
					Rules: []rbacv1.PolicyRule{
						{
							Verbs:         []string{"get"},
							ResourceNames: []string{"foo--1"},
							Resources:     []string{"workspaces"},
							APIGroups:     []string{"tenancy.kcp.dev"},
						},
						{
							Verbs:         []string{"view"},
							ResourceNames: []string{"foo--1"},
							Resources:     []string{"workspaces/content"},
							APIGroups:     []string{"tenancy.kcp.dev"},
						},
					},
				},
				rbacv1.ClusterRole{
					ObjectMeta: metav1.ObjectMeta{
						Name: "owner-workspace-foo-test-user",
						Labels: map[string]string{
							InternalNameLabel: "foo--1",
						},
					},
					Rules: []rbacv1.PolicyRule{
						{
							Verbs:         []string{"get", "delete"},
							ResourceNames: []string{"foo--1"},
							Resources:     []string{"workspaces"},
							APIGroups:     []string{"tenancy.kcp.dev"},
						},
						{
							Verbs:         []string{"view", "edit"},
							ResourceNames: []string{"foo--1"},
							Resources:     []string{"workspaces/content"},
							APIGroups:     []string{"tenancy.kcp.dev"},
						},
					},
				},
			))

			workspaceList, err := kcpClient.Tracker().List(tenancyv1alpha1.SchemeGroupVersion.WithResource("clusterworkspaces"), tenancyv1alpha1.SchemeGroupVersion.WithKind("ClusterWorkspace"), "")
			require.NoError(t, err)
			wsList := workspaceList.(*tenancyv1alpha1.ClusterWorkspaceList)
			assert.ElementsMatch(t, wsList.Items, append(testData.clusterWorkspaces,
				tenancyv1alpha1.ClusterWorkspace{
					ObjectMeta: metav1.ObjectMeta{
						Name: "foo--1",
					},
				},
			))
		},
	}
	applyTest(t, test)
}

func TestCreateWorkspacePrettyNameAlreadyExists(t *testing.T) {
	user := &kuser.DefaultInfo{
		Name:   "test-user",
		UID:    "test-uid",
		Groups: []string{"test-group"},
	}
	test := TestDescription{
		TestData: TestData{
			user:  user,
			scope: PersonalScope,
			reviewerProvider: mockReviewerProvider{
				"get":    mockReviewer{},
				"delete": mockReviewer{},
			},
			clusterWorkspaces: []tenancyv1alpha1.ClusterWorkspace{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "foo"},
				},
			},
			clusterRoleBindings: []rbacv1.ClusterRoleBinding{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: getRoleBindingName(OwnerRoleType, "foo", user),
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
			clusterRoles: []rbacv1.ClusterRole{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: getRoleBindingName(ListerRoleType, "foo", user),
						Labels: map[string]string{
							InternalNameLabel: "foo",
						},
					},
					Rules: []rbacv1.PolicyRule{
						{
							Verbs:         []string{"get"},
							ResourceNames: []string{"foo"},
							Resources:     []string{"workspaces"},
							APIGroups:     []string{"tenancy.kcp.dev"},
						},
						{
							Verbs:         []string{"view"},
							ResourceNames: []string{"foo"},
							Resources:     []string{"workspaces/content"},
							APIGroups:     []string{"tenancy.kcp.dev"},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: getRoleBindingName(OwnerRoleType, "foo", user),
						Labels: map[string]string{
							InternalNameLabel: "foo",
						},
					},
					Rules: []rbacv1.PolicyRule{
						{
							Verbs:         []string{"get", "delete"},
							ResourceNames: []string{"foo"},
							Resources:     []string{"workspaces"},
							APIGroups:     []string{"tenancy.kcp.dev"},
						},
						{
							Verbs:         []string{"view", "edit"},
							ResourceNames: []string{"foo"},
							Resources:     []string{"workspaces/content"},
							APIGroups:     []string{"tenancy.kcp.dev"},
						},
					},
				},
			},
		},
		apply: func(t *testing.T, storage *REST, kubeconfigSubResourceStorage *KubeconfigSubresourceREST, ctx context.Context, kubeClient *fake.Clientset, kcpClient *tenancyv1fake.Clientset, listerCheckedUsers func() []kuser.Info, testData TestData) {
			newWorkspace := tenancyv1beta1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "foo",
				},
			}
			response, err := storage.Create(ctx, &newWorkspace, nil, &metav1.CreateOptions{})
			require.EqualError(t, err, "workspaces.tenancy.kcp.dev \"foo\" already exists")
			require.Nil(t, response)

			crbList, err := kubeClient.Tracker().List(rbacv1.SchemeGroupVersion.WithResource("clusterrolebindings"), rbacv1.SchemeGroupVersion.WithKind("ClusterRoleBinding"), "")
			require.NoError(t, err)
			crbs := crbList.(*rbacv1.ClusterRoleBindingList)
			assert.ElementsMatch(t, crbs.Items, testData.clusterRoleBindings)
			crList, err := kubeClient.Tracker().List(rbacv1.SchemeGroupVersion.WithResource("clusterroles"), rbacv1.SchemeGroupVersion.WithKind("ClusterRole"), "")
			require.NoError(t, err)
			crs := crList.(*rbacv1.ClusterRoleList)
			assert.ElementsMatch(t, crs.Items, testData.clusterRoles)
			workspaceList, err := kcpClient.Tracker().List(tenancyv1alpha1.SchemeGroupVersion.WithResource("clusterworkspaces"), tenancyv1alpha1.SchemeGroupVersion.WithKind("ClusterWorkspace"), "")
			require.NoError(t, err)
			wsList := workspaceList.(*tenancyv1alpha1.ClusterWorkspaceList)
			assert.ElementsMatch(t, wsList.Items, testData.clusterWorkspaces)
		},
	}
	applyTest(t, test)
}

func TestDeleteWorkspaceNotFound(t *testing.T) {
	user := &kuser.DefaultInfo{
		Name:   "test-user",
		UID:    "test-uid",
		Groups: []string{"test-group"},
	}
	test := TestDescription{
		TestData: TestData{
			user:  user,
			scope: PersonalScope,
			reviewerProvider: mockReviewerProvider{
				"get":    mockReviewer{},
				"delete": mockReviewer{},
			},
			clusterWorkspaces: []tenancyv1alpha1.ClusterWorkspace{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "foo"},
				},
			},
			clusterRoleBindings: []rbacv1.ClusterRoleBinding{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: getRoleBindingName(OwnerRoleType, "foo", user),
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
			clusterRoles: []rbacv1.ClusterRole{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: getRoleBindingName(OwnerRoleType, "foo", user),
						Labels: map[string]string{
							InternalNameLabel: "foo",
						},
					},
					Rules: []rbacv1.PolicyRule{
						{
							Verbs:         []string{"get", "delete"},
							ResourceNames: []string{"foo"},
							Resources:     []string{"workspaces"},
							APIGroups:     []string{"tenancy.kcp.dev"},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: getRoleBindingName(ListerRoleType, "foo", user),
						Labels: map[string]string{
							InternalNameLabel: "foo",
						},
					},
					Rules: []rbacv1.PolicyRule{
						{
							Verbs:         []string{"get"},
							ResourceNames: []string{"foo"},
							Resources:     []string{"workspaces"},
							APIGroups:     []string{"tenancy.kcp.dev"},
						},
					},
				},
			},
		},
		apply: func(t *testing.T, storage *REST, kubeconfigSubResourceStorage *KubeconfigSubresourceREST, ctx context.Context, kubeClient *fake.Clientset, kcpClient *tenancyv1fake.Clientset, listerCheckedUsers func() []kuser.Info, testData TestData) {
			response, deletedNow, err := storage.Delete(ctx, "foo-with-does-not-exist", nil, &metav1.DeleteOptions{})
			assert.EqualError(t, err, "workspaces.tenancy.kcp.dev \"foo-with-does-not-exist\" not found")
			assert.Nil(t, response)
			assert.False(t, deletedNow)
			crbList, err := kubeClient.Tracker().List(rbacv1.SchemeGroupVersion.WithResource("clusterrolebindings"), rbacv1.SchemeGroupVersion.WithKind("ClusterRoleBinding"), "")
			require.NoError(t, err)
			crbs := crbList.(*rbacv1.ClusterRoleBindingList)
			assert.ElementsMatch(t, crbs.Items, testData.clusterRoleBindings)
			crList, err := kubeClient.Tracker().List(rbacv1.SchemeGroupVersion.WithResource("clusterroles"), rbacv1.SchemeGroupVersion.WithKind("ClusterRole"), "")
			require.NoError(t, err)
			crs := crList.(*rbacv1.ClusterRoleList)
			assert.ElementsMatch(t, crs.Items, testData.clusterRoles)
			workspaceList, err := kcpClient.Tracker().List(tenancyv1alpha1.SchemeGroupVersion.WithResource("clusterworkspaces"), tenancyv1alpha1.SchemeGroupVersion.WithKind("ClusterWorkspace"), "")
			require.NoError(t, err)
			wsList := workspaceList.(*tenancyv1alpha1.ClusterWorkspaceList)
			assert.ElementsMatch(t, wsList.Items, testData.clusterWorkspaces)
		},
	}
	applyTest(t, test)
}

func TestDeleteWorkspaceForbidden(t *testing.T) {
	user := &kuser.DefaultInfo{
		Name:   "test-user",
		UID:    "test-uid",
		Groups: []string{"test-group"},
	}
	test := TestDescription{
		TestData: TestData{
			user:  user,
			scope: PersonalScope,
			reviewerProvider: mockReviewerProvider{
				"get":    mockReviewer{},
				"delete": mockReviewer{},
			},
			clusterWorkspaces: []tenancyv1alpha1.ClusterWorkspace{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "foo"},
				},
			},
			clusterRoleBindings: []rbacv1.ClusterRoleBinding{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: getRoleBindingName(OwnerRoleType, "foo", user),
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
			clusterRoles: []rbacv1.ClusterRole{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: getRoleBindingName(OwnerRoleType, "foo", user),
						Labels: map[string]string{
							InternalNameLabel: "foo",
						},
					},
					Rules: []rbacv1.PolicyRule{
						{
							Verbs:         []string{"get", "delete"},
							ResourceNames: []string{"foo"},
							Resources:     []string{"workspaces"},
							APIGroups:     []string{"tenancy.kcp.dev"},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: getRoleBindingName(ListerRoleType, "foo", user),
						Labels: map[string]string{
							InternalNameLabel: "foo",
						},
					},
					Rules: []rbacv1.PolicyRule{
						{
							Verbs:         []string{"get"},
							ResourceNames: []string{"foo"},
							Resources:     []string{"workspaces"},
							APIGroups:     []string{"tenancy.kcp.dev"},
						},
					},
				},
			},
		},
		apply: func(t *testing.T, storage *REST, kubeconfigSubResourceStorage *KubeconfigSubresourceREST, ctx context.Context, kubeClient *fake.Clientset, kcpClient *tenancyv1fake.Clientset, listerCheckedUsers func() []kuser.Info, testData TestData) {
			response, deletedNow, err := storage.Delete(ctx, "foo", nil, &metav1.DeleteOptions{})
			assert.EqualError(t, err, "workspace.tenancy.kcp.dev is forbidden: User test-user doesn't have the permission to delete workspace foo")
			assert.Nil(t, response)
			assert.False(t, deletedNow)
			crbList, err := kubeClient.Tracker().List(rbacv1.SchemeGroupVersion.WithResource("clusterrolebindings"), rbacv1.SchemeGroupVersion.WithKind("ClusterRoleBinding"), "")
			require.NoError(t, err)
			crbs := crbList.(*rbacv1.ClusterRoleBindingList)
			assert.ElementsMatch(t, crbs.Items, testData.clusterRoleBindings)
			crList, err := kubeClient.Tracker().List(rbacv1.SchemeGroupVersion.WithResource("clusterroles"), rbacv1.SchemeGroupVersion.WithKind("ClusterRole"), "")
			require.NoError(t, err)
			crs := crList.(*rbacv1.ClusterRoleList)
			assert.ElementsMatch(t, crs.Items, testData.clusterRoles)
			workspaceList, err := kcpClient.Tracker().List(tenancyv1alpha1.SchemeGroupVersion.WithResource("clusterworkspaces"), tenancyv1alpha1.SchemeGroupVersion.WithKind("ClusterWorkspace"), "")
			require.NoError(t, err)
			wsList := workspaceList.(*tenancyv1alpha1.ClusterWorkspaceList)
			assert.ElementsMatch(t, wsList.Items, testData.clusterWorkspaces)
		},
	}
	applyTest(t, test)
}

func TestDeletePersonalWorkspace(t *testing.T) {
	user := &kuser.DefaultInfo{
		Name:   "test-user",
		UID:    "test-uid",
		Groups: []string{"test-group"},
	}
	test := TestDescription{
		TestData: TestData{
			user:  user,
			scope: PersonalScope,
			reviewerProvider: mockReviewerProvider{
				"get": mockReviewer{
					"foo": mockReview{
						users:  []string{"test-user"},
						groups: []string{""},
					},
				},
				"delete": mockReviewer{
					"foo": mockReview{
						users:  []string{"test-user"},
						groups: []string{""},
					},
				},
			},
			clusterWorkspaces: []tenancyv1alpha1.ClusterWorkspace{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "foo"},
				},
			},
			clusterRoleBindings: []rbacv1.ClusterRoleBinding{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: getRoleBindingName(OwnerRoleType, "foo", user),
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
			clusterRoles: []rbacv1.ClusterRole{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: getRoleBindingName(OwnerRoleType, "foo", user),
						Labels: map[string]string{
							InternalNameLabel: "foo",
						},
					},
					Rules: []rbacv1.PolicyRule{
						{
							Verbs:         []string{"get", "delete"},
							ResourceNames: []string{"foo"},
							Resources:     []string{"workspaces"},
							APIGroups:     []string{"tenancy.kcp.dev"},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: getRoleBindingName(ListerRoleType, "foo", user),
						Labels: map[string]string{
							InternalNameLabel: "foo",
						},
					},
					Rules: []rbacv1.PolicyRule{
						{
							Verbs:         []string{"get"},
							ResourceNames: []string{"foo"},
							Resources:     []string{"workspaces"},
							APIGroups:     []string{"tenancy.kcp.dev"},
						},
					},
				},
			},
		},
		apply: func(t *testing.T, storage *REST, kubeconfigSubResourceStorage *KubeconfigSubresourceREST, ctx context.Context, kubeClient *fake.Clientset, kcpClient *tenancyv1fake.Clientset, listerCheckedUsers func() []kuser.Info, testData TestData) {
			response, deletedNow, err := storage.Delete(ctx, "foo", nil, &metav1.DeleteOptions{})
			assert.NoError(t, err)
			assert.Nil(t, response)
			assert.False(t, deletedNow)
			crbList, err := kubeClient.Tracker().List(rbacv1.SchemeGroupVersion.WithResource("clusterrolebindings"), rbacv1.SchemeGroupVersion.WithKind("ClusterRoleBinding"), "")
			require.NoError(t, err)
			crbs := crbList.(*rbacv1.ClusterRoleBindingList)
			assert.Empty(t, crbs.Items)
			crList, err := kubeClient.Tracker().List(rbacv1.SchemeGroupVersion.WithResource("clusterroles"), rbacv1.SchemeGroupVersion.WithKind("ClusterRole"), "")
			require.NoError(t, err)
			crs := crList.(*rbacv1.ClusterRoleList)
			assert.Empty(t, crs.Items)
			workspaceList, err := kcpClient.Tracker().List(tenancyv1alpha1.SchemeGroupVersion.WithResource("clusterworkspaces"), tenancyv1alpha1.SchemeGroupVersion.WithKind("ClusterWorkspace"), "")
			require.NoError(t, err)
			wsList := workspaceList.(*tenancyv1alpha1.ClusterWorkspaceList)
			assert.Empty(t, wsList.Items)
		},
	}
	applyTest(t, test)
}

func TestDeletePersonalWorkspaceWithPrettyName(t *testing.T) {
	user := &kuser.DefaultInfo{
		Name:   "test-user",
		UID:    "test-uid",
		Groups: []string{"test-group"},
	}
	test := TestDescription{
		TestData: TestData{
			user:  user,
			scope: PersonalScope,
			reviewerProvider: mockReviewerProvider{
				"get": mockReviewer{
					"foo--1": mockReview{
						users:  []string{"test-user"},
						groups: []string{""},
					},
				},
				"delete": mockReviewer{
					"foo--1": mockReview{
						users:  []string{"test-user"},
						groups: []string{""},
					},
				},
			},
			clusterWorkspaces: []tenancyv1alpha1.ClusterWorkspace{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "foo--1"},
				},
			},
			clusterRoleBindings: []rbacv1.ClusterRoleBinding{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: getRoleBindingName(OwnerRoleType, "foo", user),
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
			clusterRoles: []rbacv1.ClusterRole{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: getRoleBindingName(OwnerRoleType, "foo", user),
						Labels: map[string]string{
							InternalNameLabel: "foo--1",
						},
					},
					Rules: []rbacv1.PolicyRule{
						{
							Verbs:         []string{"get", "delete"},
							ResourceNames: []string{"foo--1"},
							Resources:     []string{"workspaces"},
							APIGroups:     []string{"tenancy.kcp.dev"},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: getRoleBindingName(ListerRoleType, "foo", user),
						Labels: map[string]string{
							InternalNameLabel: "foo--1",
						},
					},
					Rules: []rbacv1.PolicyRule{
						{
							Verbs:         []string{"get"},
							ResourceNames: []string{"foo--1"},
							Resources:     []string{"workspaces"},
							APIGroups:     []string{"tenancy.kcp.dev"},
						},
					},
				},
			},
		},
		apply: func(t *testing.T, storage *REST, kubeconfigSubResourceStorage *KubeconfigSubresourceREST, ctx context.Context, kubeClient *fake.Clientset, kcpClient *tenancyv1fake.Clientset, listerCheckedUsers func() []kuser.Info, testData TestData) {
			response, deletedNow, err := storage.Delete(ctx, "foo", nil, &metav1.DeleteOptions{})
			assert.NoError(t, err)
			assert.Nil(t, response)
			assert.False(t, deletedNow)
			crbList, err := kubeClient.Tracker().List(rbacv1.SchemeGroupVersion.WithResource("clusterrolebindings"), rbacv1.SchemeGroupVersion.WithKind("ClusterRoleBinding"), "")
			require.NoError(t, err)
			crbs := crbList.(*rbacv1.ClusterRoleBindingList)
			assert.Empty(t, crbs.Items)
			crList, err := kubeClient.Tracker().List(rbacv1.SchemeGroupVersion.WithResource("clusterroles"), rbacv1.SchemeGroupVersion.WithKind("ClusterRole"), "")
			require.NoError(t, err)
			crs := crList.(*rbacv1.ClusterRoleList)
			assert.Empty(t, crs.Items)
			workspaceList, err := kcpClient.Tracker().List(tenancyv1alpha1.SchemeGroupVersion.WithResource("clusterworkspaces"), tenancyv1alpha1.SchemeGroupVersion.WithKind("ClusterWorkspace"), "")
			require.NoError(t, err)
			wsList := workspaceList.(*tenancyv1alpha1.ClusterWorkspaceList)
			assert.Empty(t, wsList.Items)
		},
	}
	applyTest(t, test)
}
