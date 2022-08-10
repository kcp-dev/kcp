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
	"fmt"
	"math/rand"
	"reflect"
	"testing"

	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	kuser "k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	apirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
	"k8s.io/kubernetes/pkg/controller"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	tenancyv1fake "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/fake"
	workspaceauth "github.com/kcp-dev/kcp/pkg/virtual/workspaces/authorization"
)

// mockLister returns the workspaces in the list
type mockLister struct {
	checkedUsers []kuser.Info
	workspaces   []tenancyv1alpha1.ClusterWorkspace
}

func (m *mockLister) CheckedUsers() []kuser.Info {
	return m.checkedUsers
}

func (m *mockLister) List(user kuser.Info, _ labels.Selector, _ fields.Selector) (*tenancyv1alpha1.ClusterWorkspaceList, error) {
	m.checkedUsers = append(m.checkedUsers, user)
	return &tenancyv1alpha1.ClusterWorkspaceList{
		Items: m.workspaces,
	}, nil
}

type TestData struct {
	clusterRoles           []rbacv1.ClusterRole
	clusterRoleBindings    []rbacv1.ClusterRoleBinding
	clusterWorkspaces      []tenancyv1alpha1.ClusterWorkspace
	workspaceCreationError error
	workspaceLister        *mockLister
	user                   kuser.Info
	reviewer               *workspaceauth.Reviewer
	rootReviewer           *workspaceauth.Reviewer
	orgName                logicalcluster.Name
}

type TestDescription struct {
	TestData
	apply func(t *testing.T, storage *REST, ctx context.Context, kubeClient *fake.Clientset, kcpClient *tenancyv1fake.Clientset, listerCheckedUsers func() []kuser.Info, testData TestData)
}

func applyTest(t *testing.T, test TestDescription) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	watcherStarted := make(chan struct{})

	workspaceList := tenancyv1alpha1.ClusterWorkspaceList{
		Items: test.clusterWorkspaces,
	}
	crbList := rbacv1.ClusterRoleBindingList{
		Items: test.clusterRoleBindings,
	}
	crList := rbacv1.ClusterRoleList{
		Items: test.clusterRoles,
	}
	mockKCPClient := tenancyv1fake.NewSimpleClientset(&workspaceList)
	mockKCPClient.PrependReactor("create", "clusterworkspaces", func(action clienttesting.Action) (bool, runtime.Object, error) {
		create := action.(clienttesting.CreateAction)
		workspace := create.GetObject().(*tenancyv1alpha1.ClusterWorkspace)
		for _, w := range workspaceList.Items {
			if workspace.Name == w.Name {
				return false, nil, errors.NewAlreadyExists(schema.GroupResource{}, workspace.Name)
			}
		}

		if test.workspaceCreationError != nil {
			return true, nil, test.workspaceCreationError
		}

		workspace = workspace.DeepCopy()
		if workspace.Name == "" && workspace.GenerateName != "" {
			workspace.Name = fmt.Sprintf("%s%4x", workspace.GenerateName, rand.Uint32()&65535)
			workspace.GenerateName = ""
		}

		if err := mockKCPClient.Tracker().Add(workspace); err != nil {
			return true, nil, err
		}

		return true, workspace, nil
	})
	mockKubeClient := fake.NewSimpleClientset(&crbList, &crList)
	mockKubeClient.PrependWatchReactor("*", func(action clienttesting.Action) (handled bool, ret watch.Interface, err error) {
		gvr := action.GetResource()
		ns := action.GetNamespace()
		w, err := mockKubeClient.Tracker().Watch(gvr, ns)
		if err != nil {
			return false, nil, err
		}
		close(watcherStarted)
		return true, w, nil
	})
	mockKubeClient.AddReactor("delete-collection", "*", func(action clienttesting.Action) (handled bool, ret runtime.Object, err error) {
		deleteCollectionAction := action.(clienttesting.DeleteCollectionAction)
		var gvr = deleteCollectionAction.GetResource()
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
	crbInformer := kubeInformers.Rbac().V1().ClusterRoleBindings().Informer()

	// Make sure informers are running.
	kubeInformers.Start(ctx.Done())

	// This is not required in tests, but it serves as a proof-of-concept by
	// ensuring that the informer goroutine have warmed up and called List before
	// we send any events to it.
	cache.WaitForCacheSync(ctx.Done(), crbInformer.HasSynced)

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
		getFilteredClusterWorkspaces: func(orgName logicalcluster.Name) FilteredClusterWorkspaces {
			return &clusterWorkspaces{clusterWorkspaceLister: clusterWorkspaceLister}
		},
		crbInformer:           kubeInformers.Rbac().V1().ClusterRoleBindings(),
		kubeClusterClient:     mockKubeClusterClient(func(logicalcluster.Name) kubernetes.Interface { return mockKubeClient }),
		kcpClusterClient:      mockKcpClusterClient(func(logicalcluster.Name) kcpclientset.Interface { return mockKCPClient }),
		clusterWorkspaceCache: nil,
		delegatedAuthz: func(clusterName logicalcluster.Name, client kubernetes.ClusterInterface) (authorizer.Authorizer, error) {
			if clusterName == tenancyv1alpha1.RootCluster {
				return test.rootReviewer, nil
			}
			return test.reviewer, nil
		},
	}
	ctx = apirequest.WithUser(ctx, test.user)
	ctx = apirequest.WithValue(ctx, WorkspacesOrgKey, test.orgName)

	test.apply(t, &storage, ctx, mockKubeClient, mockKCPClient, clusterWorkspaceLister.CheckedUsers, test.TestData)
}

func TestListWorkspacesWithGroupPermission(t *testing.T) {
	user := &kuser.DefaultInfo{
		Name:   "test-user",
		UID:    "test-uid",
		Groups: []string{"test-group"},
	}
	test := TestDescription{
		TestData: TestData{
			user:    user,
			orgName: logicalcluster.New("root:orgName"),
			reviewer: workspaceauth.NewReviewer(&mockSubjectLocator{
				subjects: map[string]map[string][]rbacv1.Subject{
					"list/tenancy.kcp.dev/v1alpha1/workspaces": {
						"": rbacGroups("test-group"),
					},
				},
			}),
			rootReviewer: workspaceauth.NewReviewer(nil),
			clusterWorkspaces: []tenancyv1alpha1.ClusterWorkspace{
				{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							logicalcluster.AnnotationKey: "root:orgName",
						},
						Name: "foo",
					},
				},
			},
			clusterRoleBindings: []rbacv1.ClusterRoleBinding{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: getOwnerRoleBindingName("foo", user),
						Annotations: map[string]string{
							logicalcluster.AnnotationKey: "root:orgName",
						},
						Labels: map[string]string{
							WorkspaceNameLabel: "foo",
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
		apply: func(t *testing.T, storage *REST, ctx context.Context, kubeClient *fake.Clientset, kcpClient *tenancyv1fake.Clientset, listerCheckedUsers func() []kuser.Info, testData TestData) {
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
					Groups: []string{"test-group"},
				},
				checkedUsers[0],
				"The workspaceLister should have checked the user with its groups")
		},
	}
	applyTest(t, test)
}

func TestListWorkspacesWithUserPermission(t *testing.T) {
	user := &kuser.DefaultInfo{
		Name:   "test-user",
		UID:    "test-uid",
		Groups: []string{},
	}
	test := TestDescription{
		TestData: TestData{
			user:    user,
			orgName: logicalcluster.New("root:orgName"),
			reviewer: workspaceauth.NewReviewer(&mockSubjectLocator{
				subjects: map[string]map[string][]rbacv1.Subject{
					"list/tenancy.kcp.dev/v1alpha1/workspaces": {
						"": rbacUsers("test-user"),
					},
				},
			}),
			rootReviewer: workspaceauth.NewReviewer(nil),
			clusterWorkspaces: []tenancyv1alpha1.ClusterWorkspace{
				{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							logicalcluster.AnnotationKey: "root:orgName",
						},
						Name: "foo",
					},
				},
			},
			clusterRoleBindings: []rbacv1.ClusterRoleBinding{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: getOwnerRoleBindingName("foo", user),
						Annotations: map[string]string{
							logicalcluster.AnnotationKey: "root:orgName",
						},
						Labels: map[string]string{
							WorkspaceNameLabel: "foo",
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
		apply: func(t *testing.T, storage *REST, ctx context.Context, kubeClient *fake.Clientset, kcpClient *tenancyv1fake.Clientset, listerCheckedUsers func() []kuser.Info, testData TestData) {
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
				"The workspaceLister should have checked the user")
		},
	}
	applyTest(t, test)
}

func TestListWorkspacesOnRootOrgWithPermission(t *testing.T) {
	user := &kuser.DefaultInfo{
		Name:   "test-user",
		UID:    "test-uid",
		Groups: []string{"test-group"},
	}
	test := TestDescription{
		TestData: TestData{
			user:     user,
			orgName:  logicalcluster.New("root"),
			reviewer: workspaceauth.NewReviewer(nil),
			rootReviewer: workspaceauth.NewReviewer(&mockSubjectLocator{
				subjects: map[string]map[string][]rbacv1.Subject{
					"list/tenancy.kcp.dev/v1alpha1/workspaces": {
						"": rbacGroups("test-group"),
					},
				},
			}),
			clusterWorkspaces:   []tenancyv1alpha1.ClusterWorkspace{{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{logicalcluster.AnnotationKey: "root"}, Name: "orgName"}}},
			clusterRoleBindings: []rbacv1.ClusterRoleBinding{},
		},
		apply: func(t *testing.T, storage *REST, ctx context.Context, kubeClient *fake.Clientset, kcpClient *tenancyv1fake.Clientset, listerCheckedUsers func() []kuser.Info, testData TestData) {
			response, err := storage.List(ctx, nil)
			require.NoError(t, err)
			workspaces := response.(*tenancyv1beta1.WorkspaceList)
			require.Len(t, workspaces.Items, 1, "workspaces.Items should have len 1")
			responseWorkspace := workspaces.Items[0]
			assert.Equal(t, "orgName", responseWorkspace.Name)
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

func TestGetWorkspace(t *testing.T) {
	user := &kuser.DefaultInfo{
		Name:   "test-user",
		UID:    "test-uid",
		Groups: []string{"test-group"},
	}
	test := TestDescription{
		TestData: TestData{
			user:    user,
			orgName: logicalcluster.New("root:orgName"),
			reviewer: workspaceauth.NewReviewer(&mockSubjectLocator{
				subjects: map[string]map[string][]rbacv1.Subject{
					"get/tenancy.kcp.dev/v1alpha1/workspaces": {
						"foo": rbacGroups("test-group"),
					},
				},
			}),
			rootReviewer: workspaceauth.NewReviewer(nil),
			clusterWorkspaces: []tenancyv1alpha1.ClusterWorkspace{
				{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							logicalcluster.AnnotationKey: "root:orgName",
						},
						Name: "foo",
					},
				},
			},
			clusterRoleBindings: []rbacv1.ClusterRoleBinding{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: getOwnerRoleBindingName("foo", user),
						Annotations: map[string]string{
							logicalcluster.AnnotationKey: "root:orgName",
						},
						Labels: map[string]string{
							WorkspaceNameLabel: "foo",
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
		apply: func(t *testing.T, storage *REST, ctx context.Context, kubeClient *fake.Clientset, kcpClient *tenancyv1fake.Clientset, listerCheckedUsers func() []kuser.Info, testData TestData) {
			response, err := storage.Get(ctx, "foo", nil)
			require.NoError(t, err)
			require.IsType(t, &tenancyv1beta1.Workspace{}, response)
			responseWorkspace := response.(*tenancyv1beta1.Workspace)
			assert.Equal(t, "foo", responseWorkspace.Name)
			checkedUsers := listerCheckedUsers()
			require.Len(t, checkedUsers, 0, "The workspaceLister should not have checked any user")
		},
	}
	applyTest(t, test)
}

func TestGetWorkspaceNotFoundNoPermission(t *testing.T) {
	user := &kuser.DefaultInfo{
		Name:   "test-user",
		UID:    "test-uid",
		Groups: []string{"test-group"},
	}
	test := TestDescription{
		TestData: TestData{
			user:    user,
			orgName: logicalcluster.New("root:orgName"),
			reviewer: workspaceauth.NewReviewer(&mockSubjectLocator{
				subjects: map[string]map[string][]rbacv1.Subject{
					"get/tenancy.kcp.dev/v1alpha1/workspaces": {
						"foo": rbacGroups("test-group"),
					},
				},
			}),
			rootReviewer: workspaceauth.NewReviewer(nil),
			clusterWorkspaces: []tenancyv1alpha1.ClusterWorkspace{
				{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							logicalcluster.AnnotationKey: "root:orgName",
						},
						Name: "foo",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							logicalcluster.AnnotationKey: "root:orgName",
						},
						Name: "foo2",
					},
				},
			},
			workspaceLister: &mockLister{
				workspaces: []tenancyv1alpha1.ClusterWorkspace{
					{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: map[string]string{
								logicalcluster.AnnotationKey: "root:orgName",
							},
							Name: "foo2",
						},
					},
				},
			},
			clusterRoleBindings: []rbacv1.ClusterRoleBinding{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: getOwnerRoleBindingName("foo", user),
						Annotations: map[string]string{
							logicalcluster.AnnotationKey: "root:orgName",
						},
						Labels: map[string]string{
							WorkspaceNameLabel: "foo",
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
		apply: func(t *testing.T, storage *REST, ctx context.Context, kubeClient *fake.Clientset, kcpClient *tenancyv1fake.Clientset, listerCheckedUsers func() []kuser.Info, testData TestData) {
			response, err := storage.Get(ctx, "foo", nil)
			require.NoError(t, err, "get is authorized through the delegated authorizer only, i.e. here it should be allowed")
			require.NotNil(t, response)
			checkedUsers := listerCheckedUsers()
			require.Len(t, checkedUsers, 0, "The workspaceLister should not have checked any user")
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
			user:    user,
			orgName: logicalcluster.New("root:orgName"),
			reviewer: workspaceauth.NewReviewer(&mockSubjectLocator{
				subjects: map[string]map[string][]rbacv1.Subject{
					"create/tenancy.kcp.dev/v1alpha1/workspaces": {
						"foo": rbacGroups("test-group"),
					},
				},
			}),
			rootReviewer: workspaceauth.NewReviewer(nil),
		},
		apply: func(t *testing.T, storage *REST, ctx context.Context, kubeClient *fake.Clientset, kcpClient *tenancyv1fake.Clientset, listerCheckedUsers func() []kuser.Info, testData TestData) {
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
							WorkspaceNameLabel: "foo",
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
						Name: "owner-workspace-foo-test-user",
						Labels: map[string]string{
							WorkspaceNameLabel: "foo",
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
							Verbs:         []string{"admin", "access"},
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

func TestCreateWorkspaceWithCreateAnyPermission(t *testing.T) {
	user := &kuser.DefaultInfo{
		Name:   "test-user",
		UID:    "test-uid",
		Groups: []string{"test-group"},
	}
	test := TestDescription{
		TestData: TestData{
			user:    user,
			orgName: logicalcluster.New("root:orgName"),
			reviewer: workspaceauth.NewReviewer(&mockSubjectLocator{
				subjects: map[string]map[string][]rbacv1.Subject{
					"create/tenancy.kcp.dev/v1alpha1/workspaces": {
						"": rbacGroups("test-group"),
					},
				},
			}),
			rootReviewer: workspaceauth.NewReviewer(nil),
		},
		apply: func(t *testing.T, storage *REST, ctx context.Context, kubeClient *fake.Clientset, kcpClient *tenancyv1fake.Clientset, listerCheckedUsers func() []kuser.Info, testData TestData) {
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
							WorkspaceNameLabel: "foo",
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
						Name: "owner-workspace-foo-test-user",
						Labels: map[string]string{
							WorkspaceNameLabel: "foo",
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
							Verbs:         []string{"admin", "access"},
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

func TestCreateWorkspaceCustomLocalType(t *testing.T) {
	user := &kuser.DefaultInfo{
		Name:   "test-user",
		UID:    "test-uid",
		Groups: []string{"test-group"},
	}
	test := TestDescription{
		TestData: TestData{
			user:    user,
			orgName: logicalcluster.New("root:orgName"),
			reviewer: workspaceauth.NewReviewer(&mockSubjectLocator{
				subjects: map[string]map[string][]rbacv1.Subject{
					"create/tenancy.kcp.dev/v1alpha1/workspaces": {
						"": rbacGroups("test-group"),
					},
					"use/tenancy.kcp.dev/v1alpha1/clusterworkspacetypes": {
						"custom": rbacGroups("test-group"),
					},
				},
			}),
			rootReviewer: workspaceauth.NewReviewer(nil),
		},
		apply: func(t *testing.T, storage *REST, ctx context.Context, kubeClient *fake.Clientset, kcpClient *tenancyv1fake.Clientset, listerCheckedUsers func() []kuser.Info, testData TestData) {
			newWorkspace := tenancyv1beta1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "foo",
				},
				Spec: tenancyv1beta1.WorkspaceSpec{
					Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
						Name: tenancyv1alpha1.ClusterWorkspaceTypeName("custom"),
						Path: "root:orgName",
					},
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
							WorkspaceNameLabel: "foo",
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
						Name: "owner-workspace-foo-test-user",
						Labels: map[string]string{
							WorkspaceNameLabel: "foo",
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
							Verbs:         []string{"admin", "access"},
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

func TestCreateWorkspaceNameAlreadyExists(t *testing.T) {
	user := &kuser.DefaultInfo{
		Name:   "test-user",
		UID:    "test-uid",
		Groups: []string{"test-group"},
	}
	test := TestDescription{
		TestData: TestData{
			user:    user,
			orgName: logicalcluster.New("root:orgName"),
			reviewer: workspaceauth.NewReviewer(&mockSubjectLocator{
				subjects: map[string]map[string][]rbacv1.Subject{
					"create/tenancy.kcp.dev/v1alpha1/workspaces": {
						"": rbacGroups("test-group"),
					},
				},
			}),
			rootReviewer: workspaceauth.NewReviewer(&mockSubjectLocator{
				subjects: map[string]map[string][]rbacv1.Subject{
					"use/tenancy.kcp.dev/v1alpha1/clusterworkspacetypes": {
						"universal": rbacGroups("test-group"),
					},
				},
			}),
			clusterWorkspaces: []tenancyv1alpha1.ClusterWorkspace{
				{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							logicalcluster.AnnotationKey: "root:orgName",
						},
						Name: "foo",
					},
				},
			},
			clusterRoleBindings: []rbacv1.ClusterRoleBinding{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: getOwnerRoleBindingName("foo", user),
						Annotations: map[string]string{
							logicalcluster.AnnotationKey: "root:orgName",
						},
						Labels: map[string]string{
							WorkspaceNameLabel: "foo",
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
						Name: getOwnerRoleBindingName("foo", user),
						Annotations: map[string]string{
							logicalcluster.AnnotationKey: "root:orgName",
						},
						Labels: map[string]string{
							WorkspaceNameLabel: "foo",
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
		apply: func(t *testing.T, storage *REST, ctx context.Context, kubeClient *fake.Clientset, kcpClient *tenancyv1fake.Clientset, listerCheckedUsers func() []kuser.Info, testData TestData) {
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

func TestCreateWorkspaceWithClusterWorkspaceCreationError(t *testing.T) {
	user := &kuser.DefaultInfo{
		Name:   "test-user",
		UID:    "test-uid",
		Groups: []string{"test-group"},
	}
	test := TestDescription{
		TestData: TestData{
			user:                   user,
			orgName:                logicalcluster.New("root:orgName"),
			workspaceCreationError: errors.NewBadRequest("something bad happened"),
			reviewer: workspaceauth.NewReviewer(&mockSubjectLocator{
				subjects: map[string]map[string][]rbacv1.Subject{
					"create/tenancy.kcp.dev/v1alpha1/workspaces": {
						"": rbacGroups("test-group"),
					},
				},
			}),
			rootReviewer: workspaceauth.NewReviewer(&mockSubjectLocator{
				subjects: map[string]map[string][]rbacv1.Subject{
					"use/tenancy.kcp.dev/v1alpha1/clusterworkspacetypes": {
						"universal": rbacGroups("test-group"),
					},
				},
			}),
		},
		apply: func(t *testing.T, storage *REST, ctx context.Context, kubeClient *fake.Clientset, kcpClient *tenancyv1fake.Clientset, listerCheckedUsers func() []kuser.Info, testData TestData) {
			newWorkspace := tenancyv1beta1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "foo",
				},
			}
			response, err := storage.Create(ctx, &newWorkspace, nil, &metav1.CreateOptions{})
			require.EqualError(t, err, "something bad happened")
			require.Nil(t, response)

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

func TestDeleteWorkspaceNotFound(t *testing.T) {
	user := &kuser.DefaultInfo{
		Name:   "test-user",
		UID:    "test-uid",
		Groups: []string{"test-group"},
	}
	test := TestDescription{
		TestData: TestData{
			user:    user,
			orgName: logicalcluster.New("root:orgName"),
			reviewer: workspaceauth.NewReviewer(&mockSubjectLocator{
				subjects: map[string]map[string][]rbacv1.Subject{
					"delete/tenancy.kcp.dev/v1alpha1/workspaces": {
						"": rbacGroups("test-group"),
					},
				},
			}),
			rootReviewer: workspaceauth.NewReviewer(nil),
			clusterWorkspaces: []tenancyv1alpha1.ClusterWorkspace{
				{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							logicalcluster.AnnotationKey: "root:orgName",
						},
						Name: "foo",
					},
				},
			},
			clusterRoleBindings: []rbacv1.ClusterRoleBinding{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: getOwnerRoleBindingName("foo", user),
						Annotations: map[string]string{
							logicalcluster.AnnotationKey: "root:orgName",
						},
						Labels: map[string]string{
							WorkspaceNameLabel: "foo",
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
						Name: getOwnerRoleBindingName("foo", user),
						Annotations: map[string]string{
							logicalcluster.AnnotationKey: "root:orgName",
						},
						Labels: map[string]string{
							WorkspaceNameLabel: "foo",
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
			},
		},
		apply: func(t *testing.T, storage *REST, ctx context.Context, kubeClient *fake.Clientset, kcpClient *tenancyv1fake.Clientset, listerCheckedUsers func() []kuser.Info, testData TestData) {
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

func TestDeleteWorkspace(t *testing.T) {
	user := &kuser.DefaultInfo{
		Name:   "test-user",
		UID:    "test-uid",
		Groups: []string{"test-group"},
	}
	test := TestDescription{
		TestData: TestData{
			user:    user,
			orgName: logicalcluster.New("root:orgName"),
			reviewer: workspaceauth.NewReviewer(&mockSubjectLocator{
				subjects: map[string]map[string][]rbacv1.Subject{
					"delete/tenancy.kcp.dev/v1alpha1/workspaces": {
						"foo": rbacUsers("test-user"),
					},
				},
			}),
			rootReviewer: workspaceauth.NewReviewer(&mockSubjectLocator{
				subjects: map[string]map[string][]rbacv1.Subject{
					"access/tenancy.kcp.dev/v1alpha1/workspaces/content": {
						"orgName": rbacGroups("test-group"),
					},
				},
			}),
			clusterWorkspaces: []tenancyv1alpha1.ClusterWorkspace{
				{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							logicalcluster.AnnotationKey: "root:orgName",
						},
						Name: "foo",
					},
				},
			},
			clusterRoleBindings: []rbacv1.ClusterRoleBinding{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: getOwnerRoleBindingName("foo", user),
						Annotations: map[string]string{
							logicalcluster.AnnotationKey: "root:orgName",
						},
						Labels: map[string]string{
							WorkspaceNameLabel: "foo",
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
						Name: getOwnerRoleBindingName("foo", user),
						Annotations: map[string]string{
							logicalcluster.AnnotationKey: "root:orgName",
						},
						Labels: map[string]string{
							WorkspaceNameLabel: "foo",
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
			},
		},
		apply: func(t *testing.T, storage *REST, ctx context.Context, kubeClient *fake.Clientset, kcpClient *tenancyv1fake.Clientset, listerCheckedUsers func() []kuser.Info, testData TestData) {
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

type clusterWorkspaces struct {
	clusterWorkspaceLister *mockLister
}

func (c clusterWorkspaces) Stop() {
}

func (c clusterWorkspaces) List(user kuser.Info, labelSelector labels.Selector, fieldSelector fields.Selector) (*tenancyv1alpha1.ClusterWorkspaceList, error) {
	return c.clusterWorkspaceLister.List(user, labelSelector, fieldSelector)
}

func (c clusterWorkspaces) RemoveWatcher(watcher workspaceauth.CacheWatcher) {
}

func (c clusterWorkspaces) AddWatcher(watcher workspaceauth.CacheWatcher) {
}

type mockKcpClusterClient func(cluster logicalcluster.Name) kcpclientset.Interface

func (m mockKcpClusterClient) Cluster(cluster logicalcluster.Name) kcpclientset.Interface {
	return m(cluster)
}

type mockKubeClusterClient func(cluster logicalcluster.Name) kubernetes.Interface

func (m mockKubeClusterClient) Cluster(cluster logicalcluster.Name) kubernetes.Interface {
	return m(cluster)
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
		if subject, ok := subjects[""]; ok {
			return subject, nil
		}
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
