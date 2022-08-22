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

package authorization

import (
	"strconv"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"
	"k8s.io/kubernetes/pkg/controller"

	workspaceapi "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	tenancyv1fake "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/fake"
	tenancyInformers "github.com/kcp-dev/kcp/pkg/client/informers"
	workspacelisters "github.com/kcp-dev/kcp/pkg/client/listers/tenancy/v1alpha1"
)

// common test users
var (
	alice = &user.DefaultInfo{
		Name:   "Alice",
		UID:    "alice-uid",
		Groups: []string{},
	}
	bob = &user.DefaultInfo{
		Name:   "Bob",
		UID:    "bob-uid",
		Groups: []string{"employee"},
	}
	eve = &user.DefaultInfo{
		Name:   "Eve",
		UID:    "eve-uid",
		Groups: []string{"employee"},
	}
	frank = &user.DefaultInfo{
		Name:   "Frank",
		UID:    "frank-uid",
		Groups: []string{},
	}
)

func validateList(t *testing.T, lister Lister, user user.Info, expectedSet sets.String) {
	validateListWithSelectors(t, lister, user, labels.Everything(), fields.Everything(), expectedSet)
}

func validateListWithSelectors(t *testing.T, lister Lister, user user.Info, labelSelector labels.Selector, fieldSelector fields.Selector, expectedSet sets.String) {
	workspaceList, err := lister.List(user, labelSelector, fieldSelector)
	if err != nil {
		t.Errorf("Unexpected error %v", err)
	}
	results := sets.String{}
	for _, workspace := range workspaceList.Items {
		results.Insert(workspace.Name)
	}
	if results.Len() != expectedSet.Len() || !results.HasAll(expectedSet.List()...) {
		t.Errorf("User %v, Expected: %v, Actual: %v", user.GetName(), expectedSet, results)
	}
}

type mockSubjectLocator struct {
	subjects map[string][]rbacv1.Subject
}

func (m *mockSubjectLocator) AllowedSubjects(attributes authorizer.Attributes) ([]rbacv1.Subject, error) {
	return m.subjects[attributes.GetName()], nil
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

func TestSyncWorkspace(t *testing.T) {
	workspaceList := workspaceapi.ClusterWorkspaceList{
		Items: []workspaceapi.ClusterWorkspace{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "foo", ResourceVersion: "1"},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Name: "bar", ResourceVersion: "2", Labels: map[string]string{"label": "value"}},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Name: "car", ResourceVersion: "3"},
			},
		},
	}
	mockKCPClient := tenancyv1fake.NewSimpleClientset(&workspaceList)
	mockKubeClient := fake.NewSimpleClientset()

	subjectLocator := &mockSubjectLocator{
		subjects: map[string][]rbacv1.Subject{
			"foo": append(rbacUsers(alice.GetName(), bob.GetName()), rbacGroups(eve.GetGroups()...)...),
			"bar": append(rbacUsers(frank.GetName(), eve.GetName()), rbacGroups("random")...),
			"car": {},
		},
	}

	kubeInformers := informers.NewSharedInformerFactory(mockKubeClient, controller.NoResyncPeriodFunc())
	kcpInformers := tenancyInformers.NewSharedInformerFactory(mockKCPClient, controller.NoResyncPeriodFunc())
	wsIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	wsLister := workspacelisters.NewClusterWorkspaceLister(wsIndexer)

	authorizationCache := NewAuthorizationCache(
		wsLister,
		kcpInformers.Tenancy().V1alpha1().ClusterWorkspaces().Informer(),
		NewReviewer(subjectLocator),
		authorizer.AttributesRecord{},
		kubeInformers.Rbac().V1(),
	)
	// we prime the data we need here since we are not running reflectors
	for i := range workspaceList.Items {
		_ = wsIndexer.Add(&workspaceList.Items[i])
	}

	// synchronize the cache
	authorizationCache.synchronize()

	validateList(t, authorizationCache, alice, sets.NewString("foo"))
	validateList(t, authorizationCache, bob, sets.NewString("foo"))
	validateList(t, authorizationCache, eve, sets.NewString("foo", "bar"))
	validateList(t, authorizationCache, frank, sets.NewString("bar"))

	// modify access rules
	subjectLocator.subjects["foo"] = []rbacv1.Subject{rbacUser(bob.GetName()), rbacGroup("random")}
	subjectLocator.subjects["bar"] = []rbacv1.Subject{rbacUser(alice.GetName()), rbacUser(eve.GetName()), rbacGroup("employee")}
	subjectLocator.subjects["car"] = []rbacv1.Subject{rbacUser(bob.GetName()), rbacUser(eve.GetName()), rbacGroup("employee")}

	// modify resource version on each namespace to simulate a change had occurred to force cache refresh
	for i := range workspaceList.Items {
		workspace := workspaceList.Items[i]
		oldVersion, err := strconv.Atoi(workspace.ResourceVersion)
		if err != nil {
			t.Errorf("Bad test setup, resource versions should be numbered, %v", err)
		}
		newVersion := strconv.Itoa(oldVersion + 1)
		workspace.ResourceVersion = newVersion
		_ = wsIndexer.Add(&workspace)
	}

	// now refresh the cache (which is resource version aware)
	authorizationCache.synchronize()

	// make sure new rights hold
	validateList(t, authorizationCache, alice, sets.NewString("bar"))
	validateList(t, authorizationCache, bob, sets.NewString("foo", "bar", "car"))
	validateList(t, authorizationCache, eve, sets.NewString("bar", "car"))
	validateList(t, authorizationCache, frank, sets.NewString())

	// Now test label and field selectors
	validateListWithSelectors(t, authorizationCache, bob,
		labels.SelectorFromSet(labels.Set{"label": "value"}),
		fields.Everything(),
		sets.NewString("bar"))

	validateListWithSelectors(t, authorizationCache, bob,
		labels.Everything(),
		fields.SelectorFromSet(fields.Set{"metadata.name": "foo"}),
		sets.NewString("foo"))
}
